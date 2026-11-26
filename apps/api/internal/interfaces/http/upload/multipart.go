// multipart.go 实现分片上传通道：init / PUT 分片 / GET 进度 / complete / DELETE 取消。
// 关键约束：状态迁移一律以条件更新（CAS）判定唯一赢家；complete 的 merging→completed 与
// 落位 rename 绑定，失败方不落位；GET/complete/DELETE 读会话时做惰性过期判定。
package httpupload

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	domupload "pulsefeed/internal/domain/upload"
	"pulsefeed/internal/interfaces/http/middleware"

	"github.com/gin-gonic/gin"
)

// 分片通道的阈值与回收周期；chunk_size 在建会话时固化，后续调整不影响老客户端续传。
const (
	// DefaultChunkSize 是生产环境的分片大小（8MiB）；可由选项覆盖，仅测试使用小分片。
	DefaultChunkSize int64 = 8 << 20
	// defaultActiveSessions 是单用户同时进行中的上传会话上限。
	defaultActiveSessions = 3
	// uploadTmpDirName 是上传半成品目录名，位于静态根目录之下并被全局中间件拦截。
	uploadTmpDirName = ".upload-tmp"
	// sessionTTL 是 pending/merging 会话的惰性过期阈值。
	sessionTTL = 24 * time.Hour
	// mergeTTL 是 merging 会话的卡死阈值，超过视为进程在合并中崩溃。
	mergeTTL = 30 * time.Minute
	// orphanTTL 是无对应活跃会话的临时目录回收阈值。
	orphanTTL = 24 * time.Hour
	// terminalRetention 是终态会话行的保留时长。
	terminalRetention = 7 * 24 * time.Hour
	// sweepInterval 是兜底清扫的进程内最小间隔。
	sweepInterval = 10 * time.Minute
	// mergedFileName 是合并中间文件名。
	mergedFileName = "merged.tmp"
	// maxListedMissingParts 是缺片提示最多列出的分片数。
	maxListedMissingParts = 20
)

// complete 409 的机读原因，前端据此分流而不是一律判失败。
const (
	reasonPartsIncomplete = "upload_parts_incomplete"
	reasonUploadMerging   = "upload_merging"
	reasonTerminalState   = "upload_terminal_state"
)

// Option 配置上传处理器的可选能力。
type Option func(*Handler)

// WithMultipartRepository 注入分片上传仓储；未注入时分片端点不可用。
func WithMultipartRepository(repo domupload.Repository) Option {
	return func(handler *Handler) { handler.multipart = repo }
}

// WithChunkSize 覆盖分片大小，仅用于测试用小分片构造用例。
func WithChunkSize(size int64) Option {
	return func(handler *Handler) {
		if size > 0 {
			handler.chunkSize = size
		}
	}
}

// WithActiveSessionLimit 覆盖单用户活跃会话上限，仅用于测试。
func WithActiveSessionLimit(limit int) Option {
	return func(handler *Handler) {
		if limit > 0 {
			handler.activeLimit = limit
		}
	}
}

// initMultipartRequest 是初始化会话的请求体；sha256 可选。
type initMultipartRequest struct {
	Filename string `json:"filename"`
	Kind     string `json:"kind"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

// initMultipartResponse 是初始化/复用的响应，字段与设计文档 §4.1 一致。
type initMultipartResponse struct {
	UploadID      string `json:"upload_id"`
	ChunkSize     int64  `json:"chunk_size"`
	TotalParts    int    `json:"total_parts"`
	UploadedParts []int  `json:"uploaded_parts"`
	Status        string `json:"status"`
}

// multipartStateResponse 是进度查询响应；completed 时额外带落位结果。
type multipartStateResponse struct {
	UploadID      string `json:"upload_id"`
	Kind          string `json:"kind"`
	Filename      string `json:"filename"`
	Size          int64  `json:"size"`
	ChunkSize     int64  `json:"chunk_size"`
	TotalParts    int    `json:"total_parts"`
	UploadedParts []int  `json:"uploaded_parts"`
	Status        string `json:"status"`
	URL           string `json:"url,omitempty"`
	CoverURL      string `json:"cover_url,omitempty"`
	DurationMS    int64  `json:"duration_ms,omitempty"`
}

// uploadPartResponse 是单个分片上传的响应。
type uploadPartResponse struct {
	PartNumber    int   `json:"part_number"`
	Size          int64 `json:"size"`
	ReceivedParts int   `json:"received_parts"`
}

// completeMultipartRequest 是合并请求体，可空；sha256 用于补交期望值。
type completeMultipartRequest struct {
	SHA256 string `json:"sha256"`
}

// InitMultipart 处理 POST /api/uploads/multipart：校验 → 算 total_parts → 先查复用 →
// 未命中再统计活跃会话（≥3 返 429）→ 新建会话。
func (h *Handler) InitMultipart(c *gin.Context) {
	if !h.requireRepository(c) {
		return
	}
	var req initMultipartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求格式非法")
		return
	}

	kind, _, err := resolveUploadKind(req.Kind, req.Filename)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	// 文件名会写入会话表与最终成品名，按字符数挡住超过列宽（255）的输入。
	if len([]rune(req.Filename)) > 255 {
		fail(c, http.StatusBadRequest, "文件名过长")
		return
	}
	if req.Size <= 0 {
		fail(c, http.StatusBadRequest, "素材文件为空，无法上传")
		return
	}
	if message := validateUploadSize(kind, req.Size, h.limits); message != "" {
		fail(c, http.StatusBadRequest, message)
		return
	}
	if req.SHA256 != "" && !validSHA256(req.SHA256) {
		fail(c, http.StatusBadRequest, "sha256 必须是 64 位小写十六进制")
		return
	}

	chunkSize := h.chunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	totalParts := int((req.Size + chunkSize - 1) / chunkSize)

	userID, _ := middleware.UserIDFromContext(c)
	ctx := c.Request.Context()
	// 兜底清扫只在 init 内触发，且进程内最多每 10 分钟一次，不额外起后台 goroutine。
	h.maybeSweep(ctx)

	reusable, err := h.multipart.FindReusable(ctx, userID, req.Filename, req.Size, time.Now().Add(-sessionTTL))
	if err == nil {
		parts, listErr := h.multipart.ListPartNumbers(ctx, reusable.UploadID)
		if listErr != nil {
			log.Printf("warning: 读取已传分片失败 (upload_id=%s): %v", reusable.UploadID, listErr)
			fail(c, http.StatusInternalServerError, "读取上传会话失败")
			return
		}
		ok(c, http.StatusOK, initMultipartResponse{
			UploadID:      reusable.UploadID,
			ChunkSize:     reusable.ChunkSize,
			TotalParts:    reusable.TotalParts,
			UploadedParts: parts,
			Status:        reusable.Status.String(),
		})
		return
	}
	if !errors.Is(err, domupload.ErrSessionNotFound) {
		log.Printf("warning: 查询可复用上传会话失败 (user_id=%d): %v", userID, err)
		fail(c, http.StatusInternalServerError, "初始化上传会话失败")
		return
	}

	active, err := h.multipart.CountActive(ctx, userID)
	if err != nil {
		log.Printf("warning: 统计活跃上传会话失败 (user_id=%d): %v", userID, err)
		fail(c, http.StatusInternalServerError, "初始化上传会话失败")
		return
	}
	limit := h.activeLimit
	if limit <= 0 {
		limit = defaultActiveSessions
	}
	if active >= limit {
		fail(c, http.StatusTooManyRequests, fmt.Sprintf("同时进行中的上传会话不能超过 %d 个，请稍后再试", limit))
		return
	}

	session := &domupload.Session{
		UploadID:   newUploadID(),
		UserID:     userID,
		Kind:       kind,
		Filename:   req.Filename,
		SizeBytes:  req.Size,
		ChunkSize:  chunkSize,
		TotalParts: totalParts,
		SHA256:     req.SHA256,
		Status:     domupload.StatusPending,
	}
	if err := h.multipart.CreateSession(ctx, session); err != nil {
		log.Printf("warning: 创建上传会话失败 (user_id=%d): %v", userID, err)
		fail(c, http.StatusInternalServerError, "初始化上传会话失败")
		return
	}

	ok(c, http.StatusCreated, initMultipartResponse{
		UploadID:      session.UploadID,
		ChunkSize:     session.ChunkSize,
		TotalParts:    session.TotalParts,
		UploadedParts: []int{},
		Status:        session.Status.String(),
	})
}

// UploadPart 处理 PUT /api/uploads/multipart/:uploadId/parts/:partNumber。
// 超限时不保留 .tmp：先删除已写入的部分再返回 413。
func (h *Handler) UploadPart(c *gin.Context) {
	if !h.requireRepository(c) {
		return
	}
	uploadID := c.Param("uploadId")
	partNumber, err := strconv.Atoi(c.Param("partNumber"))
	if err != nil || partNumber <= 0 {
		fail(c, http.StatusBadRequest, "分片号非法")
		return
	}

	session, okSession := h.loadSession(c, uploadID)
	if !okSession {
		return
	}
	switch session.Status {
	case domupload.StatusPending:
	case domupload.StatusMerging:
		failWithReason(c, http.StatusConflict, "上传正在合并中，无法继续上传分片", reasonUploadMerging)
		return
	default:
		failWithReason(c, http.StatusConflict, terminalMessage(session), reasonTerminalState)
		return
	}
	if partNumber > session.TotalParts {
		fail(c, http.StatusBadRequest, fmt.Sprintf("分片号超出范围（1-%d）", session.TotalParts))
		return
	}

	chunkSize := session.ChunkSize
	if chunkSize <= 0 {
		chunkSize = h.chunkSize
	}
	expected := chunkSize
	if partNumber == session.TotalParts {
		expected = session.SizeBytes - int64(session.TotalParts-1)*chunkSize
	}

	// 有 Content-Length 时先预检；chunked 没有该头，只能靠 MaxBytesReader 读满上限中断。
	limit := chunkSize + 1
	if c.Request.ContentLength > limit {
		failOversizedPart(c, chunkSize)
		return
	}
	body := http.MaxBytesReader(c.Writer, c.Request.Body, limit)

	dir := h.tempDir(uploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("warning: 创建分片目录失败 (upload_id=%s): %v", uploadID, err)
		fail(c, http.StatusInternalServerError, "保存分片失败")
		return
	}
	tmpPath := filepath.Join(dir, partTempFileName(partNumber))
	finalPath := filepath.Join(dir, partFileName(partNumber))
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("warning: 打开分片临时文件失败 (upload_id=%s): %v", uploadID, err)
		fail(c, http.StatusInternalServerError, "保存分片失败")
		return
	}

	written, copyErr := io.Copy(file, body)
	if copyErr != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		var maxErr *http.MaxBytesError
		if errors.As(copyErr, &maxErr) {
			failOversizedPart(c, chunkSize)
			return
		}
		log.Printf("warning: 写入分片失败 (upload_id=%s part=%d): %v", uploadID, partNumber, copyErr)
		fail(c, http.StatusInternalServerError, "分片上传失败，请重试")
		return
	}
	if written != expected {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		fail(c, http.StatusBadRequest, fmt.Sprintf("分片大小与预期不符（期望 %d 字节，实际 %d 字节）", expected, written))
		return
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		fail(c, http.StatusInternalServerError, "保存分片失败")
		return
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		fail(c, http.StatusInternalServerError, "保存分片失败")
		return
	}
	if err := replaceFile(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		log.Printf("warning: 分片落位失败 (upload_id=%s part=%d): %v", uploadID, partNumber, err)
		fail(c, http.StatusInternalServerError, "保存分片失败")
		return
	}

	received, err := h.multipart.UpsertPart(c.Request.Context(), uploadID, partNumber, written)
	if err != nil {
		_ = os.Remove(finalPath)
		log.Printf("warning: 记录分片元数据失败 (upload_id=%s part=%d): %v", uploadID, partNumber, err)
		fail(c, http.StatusInternalServerError, "保存分片失败")
		return
	}

	ok(c, http.StatusOK, uploadPartResponse{PartNumber: partNumber, Size: written, ReceivedParts: received})
}

// GetMultipart 处理 GET /api/uploads/multipart/:uploadId：用于续传，读时做惰性过期判定。
func (h *Handler) GetMultipart(c *gin.Context) {
	if !h.requireRepository(c) {
		return
	}
	session, okSession := h.loadSession(c, c.Param("uploadId"))
	if !okSession {
		return
	}
	session, okSession = h.expireIfStale(c, session)
	if !okSession {
		return
	}

	parts, err := h.multipart.ListPartNumbers(c.Request.Context(), session.UploadID)
	if err != nil {
		log.Printf("warning: 读取分片列表失败 (upload_id=%s): %v", session.UploadID, err)
		fail(c, http.StatusInternalServerError, "读取上传会话失败")
		return
	}
	view := multipartStateResponse{
		UploadID:      session.UploadID,
		Kind:          session.Kind,
		Filename:      session.Filename,
		Size:          session.SizeBytes,
		ChunkSize:     session.ChunkSize,
		TotalParts:    session.TotalParts,
		UploadedParts: parts,
		Status:        session.Status.String(),
	}
	if session.Status == domupload.StatusCompleted {
		view.URL = session.URL
		view.CoverURL = session.CoverURL
		view.DurationMS = session.DurationMS
	}
	ok(c, http.StatusOK, view)
}

// CompleteMultipart 处理 POST /api/uploads/multipart/:uploadId/complete。
// CAS 失败必须重读状态分流；merging→completed 与 rename 绑定，0 行不落位；重复 complete 幂等。
func (h *Handler) CompleteMultipart(c *gin.Context) {
	if !h.requireRepository(c) {
		return
	}
	uploadID := c.Param("uploadId")
	session, okSession := h.loadSession(c, uploadID)
	if !okSession {
		return
	}
	session, okSession = h.expireIfStale(c, session)
	if !okSession {
		return
	}

	var req completeMultipartRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		fail(c, http.StatusBadRequest, "请求格式非法")
		return
	}
	if req.SHA256 != "" && !validSHA256(req.SHA256) {
		fail(c, http.StatusBadRequest, "sha256 必须是 64 位小写十六进制")
		return
	}

	ctx := c.Request.Context()
	started := false
	var fresh *domupload.Session
	for attempt := 0; attempt < 2 && !started; attempt++ {
		done, err := h.multipart.MarkMerging(ctx, uploadID)
		if err != nil {
			log.Printf("warning: 抢占合并权失败 (upload_id=%s): %v", uploadID, err)
			fail(c, http.StatusInternalServerError, "合并上传失败")
			return
		}
		if done {
			started = true
			break
		}
		fresh, err = h.multipart.GetSession(ctx, uploadID)
		if err != nil {
			log.Printf("warning: 重读上传会话失败 (upload_id=%s): %v", uploadID, err)
			fail(c, http.StatusInternalServerError, "读取上传会话失败")
			return
		}
		if fresh.Status != domupload.StatusPending {
			break
		}
	}
	if !started {
		if fresh != nil && fresh.Status != domupload.StatusPending {
			h.respondSessionState(c, fresh)
			return
		}
		failWithReason(c, http.StatusConflict, "上传正在处理中，请稍后查询进度", reasonUploadMerging)
		return
	}

	parts, err := h.multipart.ListPartNumbers(ctx, uploadID)
	if err != nil {
		log.Printf("warning: 读取分片列表失败 (upload_id=%s): %v", uploadID, err)
		_, _ = h.multipart.MarkPending(ctx, uploadID)
		fail(c, http.StatusInternalServerError, "合并上传失败")
		return
	}
	dir := h.tempDir(uploadID)
	missing := missingPartNumbers(parts, session.TotalParts)
	if len(missing) == 0 {
		for number := 1; number <= session.TotalParts; number++ {
			if _, statErr := os.Stat(filepath.Join(dir, partFileName(number))); statErr != nil {
				if errors.Is(statErr, os.ErrNotExist) {
					missing = append(missing, number)
					continue
				}
				log.Printf("warning: 检查分片文件失败 (upload_id=%s part=%d): %v", uploadID, number, statErr)
				_, _ = h.multipart.MarkPending(ctx, uploadID)
				fail(c, http.StatusInternalServerError, "读取分片失败")
				return
			}
		}
	}
	if len(missing) > 0 {
		// 缺片期间可能已被取消/过期抢占：先重读终态，避免把已取消的会话误报为缺片。
		if freshSession, getErr := h.multipart.GetSession(ctx, uploadID); getErr == nil &&
			(freshSession.Status == domupload.StatusCanceled || freshSession.Status == domupload.StatusExpired ||
				freshSession.Status == domupload.StatusFailed || freshSession.Status == domupload.StatusCompleted) {
			h.purgeTempDir(uploadID)
			h.respondSessionState(c, freshSession)
			return
		}
		// 回滚到 pending：文档 §8.3 要求前端在缺片 409 后补传缺失分片再 complete，
		// 只有 pending 才允许 PUT，因此这里必须释放合并权。
		_, _ = h.multipart.MarkPending(ctx, uploadID)
		failWithReason(c, http.StatusConflict, incompleteMessage(missing), reasonPartsIncomplete)
		return
	}

	actualSHA, total, err := h.mergeParts(dir, session)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 取消/清理先赢：目录或分片已被删除，视为会话已取消，静默中止，不返回 500。
			h.purgeTempDir(uploadID)
			freshSession, getErr := h.multipart.GetSession(ctx, uploadID)
			if getErr == nil {
				h.respondSessionState(c, freshSession)
				return
			}
			failWithReason(c, http.StatusConflict, "上传已取消", reasonTerminalState)
			return
		}
		log.Printf("warning: 合并分片失败 (upload_id=%s): %v", uploadID, err)
		h.failMerge(c, session, "合并分片失败", http.StatusInternalServerError)
		return
	}
	if total != session.SizeBytes {
		h.failMerge(c, session, "分片总大小与声明不一致", http.StatusBadRequest)
		return
	}

	// 校验期望哈希：请求体优先，其次会话列；请求体提供而会话列为空时条件回填。
	expected := req.SHA256
	if expected == "" {
		expected = session.SHA256
	} else if session.SHA256 == "" {
		if err := h.multipart.UpdateSHA256IfEmpty(ctx, uploadID, req.SHA256); err != nil {
			log.Printf("warning: 回填 sha256 失败 (upload_id=%s): %v", uploadID, err)
		}
	}
	if expected != "" && !strings.EqualFold(expected, actualSHA) {
		h.failMerge(c, session, "文件内容校验失败（SHA-256 不符）", http.StatusBadRequest)
		return
	}

	ext := strings.ToLower(filepath.Ext(session.Filename))
	durationMS, message := validateMediaContent(ctx, h.prober, filepath.Join(dir, mergedFileName), session.Kind, ext, h.limits)
	if message != "" {
		h.failMerge(c, session, message, http.StatusBadRequest)
		return
	}

	// 条件更新绑定「状态迁移」与「落位」：0 行说明已被取消/过期抢占，不执行 rename。
	filename := fmt.Sprintf("%s-%s%s", time.Now().Format("20060102150405"), randomSuffix(), ext)
	destDir := filepath.Join(h.uploadDir, session.Kind)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		log.Printf("warning: 创建成品目录失败: %v", err)
		h.failMerge(c, session, "保存上传文件失败", http.StatusInternalServerError)
		return
	}
	url := "/uploads/" + session.Kind + "/" + filename
	completed, err := h.multipart.MarkCompleted(ctx, uploadID, url, durationMS)
	if err != nil {
		log.Printf("warning: 置完成状态失败 (upload_id=%s): %v", uploadID, err)
		h.failMerge(c, session, "保存上传文件失败", http.StatusInternalServerError)
		return
	}
	if !completed {
		h.purgeTempDir(uploadID)
		freshSession, getErr := h.multipart.GetSession(ctx, uploadID)
		if getErr != nil {
			log.Printf("warning: 重读上传会话失败 (upload_id=%s): %v", uploadID, getErr)
			fail(c, http.StatusInternalServerError, "读取上传会话失败")
			return
		}
		h.respondSessionState(c, freshSession)
		return
	}

	dest := filepath.Join(destDir, filename)
	if err := os.Rename(filepath.Join(dir, mergedFileName), dest); err != nil {
		log.Printf("warning: 成品落位失败 (upload_id=%s): %v", uploadID, err)
		if _, revertErr := h.multipart.RevertCompletedToFailed(ctx, uploadID, "保存上传文件失败"); revertErr != nil {
			log.Printf("warning: 回滚完成状态失败 (upload_id=%s): %v", uploadID, revertErr)
		}
		h.purgeTempDir(uploadID)
		fail(c, http.StatusInternalServerError, "保存上传文件失败")
		return
	}

	coverURL := ""
	if session.Kind == "video" {
		coverURL, _ = h.enrichVideo(ctx, dest, strings.TrimSuffix(filename, ext)+".jpg", durationMS)
		if coverURL != "" {
			if err := h.multipart.UpdateCoverURL(ctx, uploadID, coverURL); err != nil {
				log.Printf("warning: 写入封面地址失败 (upload_id=%s): %v", uploadID, err)
			}
		}
	}
	h.purgeTempDir(uploadID)

	ok(c, http.StatusCreated, uploadResponse{
		URL:        url,
		Kind:       session.Kind,
		CoverURL:   coverURL,
		DurationMS: durationMS,
	})
}

// CancelMultipart 处理 DELETE /api/uploads/multipart/:uploadId。
// 先 CAS 再删目录；completed 返回 409 且不动成品文件。
func (h *Handler) CancelMultipart(c *gin.Context) {
	if !h.requireRepository(c) {
		return
	}
	uploadID := c.Param("uploadId")
	session, okSession := h.loadSession(c, uploadID)
	if !okSession {
		return
	}
	session, okSession = h.expireIfStale(c, session)
	if !okSession {
		return
	}

	switch session.Status {
	case domupload.StatusCanceled, domupload.StatusFailed:
		ok(c, http.StatusOK, gin.H{"upload_id": uploadID, "status": session.Status.String()})
		return
	case domupload.StatusCompleted:
		fail(c, http.StatusConflict, "上传已完成，无法取消")
		return
	case domupload.StatusExpired:
		fail(c, http.StatusGone, "上传会话已过期")
		return
	}

	ctx := c.Request.Context()
	for attempt := 0; attempt < 2; attempt++ {
		done, err := h.multipart.MarkCanceled(ctx, uploadID)
		if err != nil {
			log.Printf("warning: 置取消状态失败 (upload_id=%s): %v", uploadID, err)
			fail(c, http.StatusInternalServerError, "取消上传失败")
			return
		}
		if done {
			h.purgeTempDir(uploadID)
			ok(c, http.StatusOK, gin.H{"upload_id": uploadID, "status": domupload.StatusCanceled.String()})
			return
		}
		fresh, err := h.multipart.GetSession(ctx, uploadID)
		if err != nil {
			log.Printf("warning: 重读上传会话失败 (upload_id=%s): %v", uploadID, err)
			fail(c, http.StatusInternalServerError, "读取上传会话失败")
			return
		}
		switch fresh.Status {
		case domupload.StatusCanceled, domupload.StatusFailed:
			ok(c, http.StatusOK, gin.H{"upload_id": uploadID, "status": fresh.Status.String()})
			return
		case domupload.StatusCompleted:
			fail(c, http.StatusConflict, "上传已完成，无法取消")
			return
		case domupload.StatusExpired:
			fail(c, http.StatusGone, "上传会话已过期")
			return
		}
	}
	failWithReason(c, http.StatusConflict, "上传状态正在变化，请稍后重试", reasonUploadMerging)
}

// requireRepository 检查分片仓储是否已装配，未装配时统一返回 500。
func (h *Handler) requireRepository(c *gin.Context) bool {
	if h.multipart == nil {
		fail(c, http.StatusInternalServerError, "分片上传不可用")
		return false
	}
	return true
}

// loadSession 读取会话并校验归属：不存在 404、非本人 403。
func (h *Handler) loadSession(c *gin.Context, uploadID string) (*domupload.Session, bool) {
	if uploadID == "" {
		fail(c, http.StatusNotFound, "上传会话不存在")
		return nil, false
	}
	session, err := h.multipart.GetSession(c.Request.Context(), uploadID)
	if errors.Is(err, domupload.ErrSessionNotFound) {
		fail(c, http.StatusNotFound, "上传会话不存在")
		return nil, false
	}
	if err != nil {
		log.Printf("warning: 读取上传会话失败 (upload_id=%s): %v", uploadID, err)
		fail(c, http.StatusInternalServerError, "读取上传会话失败")
		return nil, false
	}
	userID, exists := middleware.UserIDFromContext(c)
	if !exists || session.UserID != userID {
		fail(c, http.StatusForbidden, "无权访问该上传会话")
		return nil, false
	}
	return session, true
}

// expireIfStale 做读时惰性过期判定：pending/merging 超过 24h 先条件置 expired 再返回 410。
func (h *Handler) expireIfStale(c *gin.Context, session *domupload.Session) (*domupload.Session, bool) {
	if session.Status != domupload.StatusPending && session.Status != domupload.StatusMerging {
		return session, true
	}
	now := time.Now()
	if now.Sub(session.UpdatedAt) <= sessionTTL {
		return session, true
	}
	ctx := c.Request.Context()
	if _, err := h.multipart.ExpireIfStale(ctx, session.UploadID, now.Add(-sessionTTL)); err != nil {
		log.Printf("warning: 条件置过期失败 (upload_id=%s): %v", session.UploadID, err)
		fail(c, http.StatusInternalServerError, "读取上传会话失败")
		return nil, false
	}
	fresh, err := h.multipart.GetSession(ctx, session.UploadID)
	if err != nil {
		log.Printf("warning: 重读上传会话失败 (upload_id=%s): %v", session.UploadID, err)
		fail(c, http.StatusInternalServerError, "读取上传会话失败")
		return nil, false
	}
	if fresh.Status == domupload.StatusExpired {
		fail(c, http.StatusGone, "上传会话已过期，请重新上传")
		return nil, false
	}
	return fresh, true
}

// respondSessionState 按重读到的会话状态返回：completed 返回已存结果，其余按终态分流。
func (h *Handler) respondSessionState(c *gin.Context, session *domupload.Session) {
	switch session.Status {
	case domupload.StatusCompleted:
		ok(c, http.StatusOK, uploadResponse{
			URL:        session.URL,
			Kind:       session.Kind,
			CoverURL:   session.CoverURL,
			DurationMS: session.DurationMS,
		})
	case domupload.StatusMerging:
		failWithReason(c, http.StatusConflict, "上传正在合并中，请稍后查询进度", reasonUploadMerging)
	case domupload.StatusExpired:
		fail(c, http.StatusGone, "上传会话已过期，请重新上传")
	case domupload.StatusFailed, domupload.StatusCanceled:
		failWithReason(c, http.StatusConflict, terminalMessage(session), reasonTerminalState)
	default:
		failWithReason(c, http.StatusConflict, "上传状态正在变化，请稍后重试", reasonUploadMerging)
	}
}

// failMerge 把会话置失败并清理临时目录，再返回可读错误。
func (h *Handler) failMerge(c *gin.Context, session *domupload.Session, message string, status int) {
	if _, err := h.multipart.MarkFailed(c.Request.Context(), session.UploadID, message); err != nil {
		log.Printf("warning: 置失败状态失败 (upload_id=%s): %v", session.UploadID, err)
	}
	h.purgeTempDir(session.UploadID)
	fail(c, status, message)
}

// mergeParts 按分片号顺序流式合并到 merged.tmp，同时计算 SHA-256 与总字节数。
// 分片文件缺失时返回 os.ErrNotExist，由调用方按「会话已被取消」处理。
func (h *Handler) mergeParts(dir string, session *domupload.Session) (string, int64, error) {
	mergedPath := filepath.Join(dir, mergedFileName)
	dest, err := os.OpenFile(mergedPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", 0, err
	}
	hasher := sha256.New()
	var total int64
	for number := 1; number <= session.TotalParts; number++ {
		source, openErr := os.Open(filepath.Join(dir, partFileName(number)))
		if openErr != nil {
			_ = dest.Close()
			return "", 0, openErr
		}
		copied, copyErr := io.Copy(io.MultiWriter(dest, hasher), source)
		_ = source.Close()
		if copyErr != nil {
			_ = dest.Close()
			return "", 0, copyErr
		}
		total += copied
	}
	if err := dest.Sync(); err != nil {
		_ = dest.Close()
		return "", 0, err
	}
	if err := dest.Close(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), total, nil
}

// maybeSweep 触发兜底清扫：进程内最多每 10 分钟一次，TryLock 避免并发重复执行。
func (h *Handler) maybeSweep(ctx context.Context) {
	now := time.Now()
	h.sweepStateMu.Lock()
	if now.Sub(h.lastSweep) < sweepInterval {
		h.sweepStateMu.Unlock()
		return
	}
	h.lastSweep = now
	h.sweepStateMu.Unlock()

	if !h.sweepRun.TryLock() {
		return
	}
	defer h.sweepRun.Unlock()
	h.sweep(ctx, now)
}

// sweep 执行兜底清理：过期 pending、卡死 merging、无会话孤儿目录与超期终态行。
func (h *Handler) sweep(ctx context.Context, now time.Time) {
	stale, err := h.multipart.ListStale(ctx, now.Add(-sessionTTL), now.Add(-mergeTTL))
	if err != nil {
		log.Printf("warning: 清扫读取过期上传会话失败: %v", err)
		return
	}
	for i := range stale {
		session := stale[i]
		switch session.Status {
		case domupload.StatusPending:
			done, err := h.multipart.ExpireIfStale(ctx, session.UploadID, now.Add(-sessionTTL))
			if err != nil {
				log.Printf("warning: 清扫置过期失败 (upload_id=%s): %v", session.UploadID, err)
				continue
			}
			if done {
				h.purgeTempDir(session.UploadID)
			}
		case domupload.StatusMerging:
			done, err := h.multipart.FailIfMergingStale(ctx, session.UploadID, now.Add(-mergeTTL))
			if err != nil {
				log.Printf("warning: 清扫置失败失败 (upload_id=%s): %v", session.UploadID, err)
				continue
			}
			if done {
				h.purgeTempDir(session.UploadID)
			}
		}
	}

	active, err := h.multipart.ActiveUploadIDs(ctx)
	if err != nil {
		log.Printf("warning: 清扫读取活跃上传会话失败: %v", err)
		return
	}
	entries, err := os.ReadDir(h.tempRoot())
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("warning: 读取上传临时目录失败: %v", err)
		}
	} else {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			// 仍活跃的会话一律保留；其余目录在超过 24h 后回收，
			// 同时兜住 failed/canceled/expired 会话异常残留的目录。
			if _, activeID := active[entry.Name()]; activeID {
				continue
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}
			if now.Sub(info.ModTime()) < orphanTTL {
				continue
			}
			if removeErr := os.RemoveAll(filepath.Join(h.tempRoot(), entry.Name())); removeErr != nil {
				log.Printf("warning: 清理孤儿上传目录失败 (%s): %v", entry.Name(), removeErr)
			}
		}
	}

	if err := h.multipart.DeleteTerminalBefore(ctx, now.Add(-terminalRetention)); err != nil {
		log.Printf("warning: 清理终态上传会话失败: %v", err)
	}
}

// purgeTempDir 删除会话的临时目录；目录不存在视为成功。
func (h *Handler) purgeTempDir(uploadID string) {
	if err := os.RemoveAll(h.tempDir(uploadID)); err != nil {
		log.Printf("warning: 清理上传临时目录失败 (upload_id=%s): %v", uploadID, err)
	}
}

// tempRoot 返回分片临时文件根目录。
func (h *Handler) tempRoot() string {
	return filepath.Join(h.uploadDir, uploadTmpDirName)
}

// tempDir 返回指定会话的临时目录。
func (h *Handler) tempDir(uploadID string) string {
	return filepath.Join(h.tempRoot(), uploadID)
}

// partFileName 返回正式分片文件名（六位补零）。
func partFileName(partNumber int) string {
	return fmt.Sprintf("%06d.part", partNumber)
}

// partTempFileName 返回写入中分片文件名。
func partTempFileName(partNumber int) string {
	return partFileName(partNumber) + ".tmp"
}

// replaceFile 用 rename 原子落位；Windows 下目标已存在时先删除再重试一次。
func replaceFile(source, dest string) error {
	if err := os.Rename(source, dest); err == nil {
		return nil
	}
	if removeErr := os.Remove(dest); removeErr != nil && !os.IsNotExist(removeErr) {
		return removeErr
	}
	return os.Rename(source, dest)
}

// missingPartNumbers 返回 1..total 中尚未上传的分片号，按升序。
func missingPartNumbers(uploaded []int, total int) []int {
	present := make(map[int]struct{}, len(uploaded))
	for _, number := range uploaded {
		present[number] = struct{}{}
	}
	missing := make([]int, 0)
	for number := 1; number <= total; number++ {
		if _, exists := present[number]; !exists {
			missing = append(missing, number)
		}
	}
	return missing
}

// incompleteMessage 生成缺片提示：最多列出 20 个分片号，超出以「等 N 片」表述。
func incompleteMessage(missing []int) string {
	listed := missing
	suffix := ""
	if len(missing) > maxListedMissingParts {
		listed = missing[:maxListedMissingParts]
		suffix = fmt.Sprintf(" 等 %d 片", len(missing))
	}
	numbers := make([]string, 0, len(listed))
	for _, number := range listed {
		numbers = append(numbers, strconv.Itoa(number))
	}
	return fmt.Sprintf("分片尚未上传完整，缺少分片：%s%s", strings.Join(numbers, ","), suffix)
}

// terminalMessage 生成终态会话的可读原因。
func terminalMessage(session *domupload.Session) string {
	if session.ErrorMessage != "" {
		return "上传已终止：" + session.ErrorMessage
	}
	if session.Status == domupload.StatusCanceled {
		return "上传已取消，请重新选择文件"
	}
	return "上传会话已结束，请重新选择文件"
}

// failWithReason 在错误响应 data.reason 中给出可机读原因，HTTP code 与状态码一致。
func failWithReason(c *gin.Context, status int, message, reason string) {
	c.JSON(status, gin.H{
		"code":    status,
		"message": message,
		"data":    gin.H{"reason": reason},
	})
}

// failOversizedPart 返回分片超限错误；已写入的 .tmp 由调用方负责删除。
func failOversizedPart(c *gin.Context, chunkSize int64) {
	fail(c, http.StatusRequestEntityTooLarge, fmt.Sprintf("分片大小超过上限（%d 字节）", chunkSize))
}

// newUploadID 生成 32 位小写十六进制随机串；随机源失败时退化为时间戳拼接。
func newUploadID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
