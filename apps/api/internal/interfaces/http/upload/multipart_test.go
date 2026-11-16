// multipart_test.go 覆盖分片通道的核心契约：正常/乱序/重复/续传、complete 幂等与 CAS 分流、
// 缺片回滚、取消与合并竞争、读时惰性过期、兜底清扫、超限 413 与初始化校验。
package httpupload

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	domupload "pulsefeed/internal/domain/upload"
	"pulsefeed/internal/infra/media"
	"pulsefeed/internal/interfaces/http/middleware"

	"github.com/gin-gonic/gin"
)

// fakeUploadRepo 是内存仓储：所有状态迁移按条件更新语义实现，供并发/竞争用例断言分流。
type fakeUploadRepo struct {
	mu            sync.Mutex
	sessions      map[string]*domupload.Session
	parts         map[string]map[int]int64
	onMarkMerging func()
}

// newFakeUploadRepo 创建空的内存仓储。
func newFakeUploadRepo() *fakeUploadRepo {
	return &fakeUploadRepo{
		sessions: make(map[string]*domupload.Session),
		parts:    make(map[string]map[int]int64),
	}
}

// copySession 复制会话，避免测试与处理器共享同一指针。
func copySession(session *domupload.Session) domupload.Session {
	return *session
}

// CreateSession 写入一条会话并补上创建/更新时间。
func (r *fakeUploadRepo) CreateSession(_ context.Context, session *domupload.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	clone := copySession(session)
	clone.CreatedAt = now
	clone.UpdatedAt = now
	r.sessions[clone.UploadID] = &clone
	return nil
}

// GetSession 读取会话副本。
func (r *fakeUploadRepo) GetSession(_ context.Context, uploadID string) (*domupload.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, exists := r.sessions[uploadID]
	if !exists {
		return nil, domupload.ErrSessionNotFound
	}
	clone := copySession(session)
	return &clone, nil
}

// FindReusable 按同用户 + 同文件名 + 同大小 + pending + 未过期查找。
func (r *fakeUploadRepo) FindReusable(_ context.Context, userID int64, filename string, sizeBytes int64, updatedAfter time.Time) (*domupload.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, session := range r.sessions {
		if session.UserID != userID || session.Filename != filename || session.SizeBytes != sizeBytes {
			continue
		}
		if session.Status != domupload.StatusPending || session.UpdatedAt.Before(updatedAfter) {
			continue
		}
		clone := copySession(session)
		return &clone, nil
	}
	return nil, domupload.ErrSessionNotFound
}

// CountActive 统计 pending/merging 会话数。
func (r *fakeUploadRepo) CountActive(_ context.Context, userID int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, session := range r.sessions {
		if session.UserID == userID && (session.Status == domupload.StatusPending || session.Status == domupload.StatusMerging) {
			count++
		}
	}
	return count, nil
}

// ListStale 返回超期的 pending 与卡死的 merging。
func (r *fakeUploadRepo) ListStale(_ context.Context, pendingBefore, mergingBefore time.Time) ([]domupload.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stale := make([]domupload.Session, 0)
	for _, session := range r.sessions {
		if session.Status == domupload.StatusPending && session.UpdatedAt.Before(pendingBefore) {
			stale = append(stale, copySession(session))
		}
		if session.Status == domupload.StatusMerging && session.UpdatedAt.Before(mergingBefore) {
			stale = append(stale, copySession(session))
		}
	}
	return stale, nil
}

// ExpireIfStale 条件置过期。
func (r *fakeUploadRepo) ExpireIfStale(_ context.Context, uploadID string, staleBefore time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, exists := r.sessions[uploadID]
	if !exists || session.UpdatedAt.After(staleBefore) {
		return false, nil
	}
	if session.Status != domupload.StatusPending && session.Status != domupload.StatusMerging {
		return false, nil
	}
	session.Status = domupload.StatusExpired
	session.UpdatedAt = time.Now()
	return true, nil
}

// FailIfMergingStale 条件置 merging 失败。
func (r *fakeUploadRepo) FailIfMergingStale(_ context.Context, uploadID string, staleBefore time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, exists := r.sessions[uploadID]
	if !exists || session.Status != domupload.StatusMerging || session.UpdatedAt.After(staleBefore) {
		return false, nil
	}
	session.Status = domupload.StatusFailed
	session.ErrorMessage = "合并超时"
	session.UpdatedAt = time.Now()
	return true, nil
}

// MarkMerging 条件迁移 pending → merging，并在迁移后触发测试钩子。
func (r *fakeUploadRepo) MarkMerging(_ context.Context, uploadID string) (bool, error) {
	r.mu.Lock()
	session, exists := r.sessions[uploadID]
	if !exists || session.Status != domupload.StatusPending {
		r.mu.Unlock()
		return false, nil
	}
	session.Status = domupload.StatusMerging
	session.UpdatedAt = time.Now()
	hook := r.onMarkMerging
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return true, nil
}

// MarkPending 条件回滚 merging → pending。
func (r *fakeUploadRepo) MarkPending(_ context.Context, uploadID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, exists := r.sessions[uploadID]
	if !exists || session.Status != domupload.StatusMerging {
		return false, nil
	}
	session.Status = domupload.StatusPending
	session.UpdatedAt = time.Now()
	return true, nil
}

// MarkCompleted 条件迁移 merging → completed 并写入结果。
func (r *fakeUploadRepo) MarkCompleted(_ context.Context, uploadID, url string, durationMS int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, exists := r.sessions[uploadID]
	if !exists || session.Status != domupload.StatusMerging {
		return false, nil
	}
	session.Status = domupload.StatusCompleted
	session.URL = url
	session.DurationMS = durationMS
	session.UpdatedAt = time.Now()
	return true, nil
}

// RevertCompletedToFailed 条件回滚 completed → failed。
func (r *fakeUploadRepo) RevertCompletedToFailed(_ context.Context, uploadID, reason string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, exists := r.sessions[uploadID]
	if !exists || session.Status != domupload.StatusCompleted {
		return false, nil
	}
	session.Status = domupload.StatusFailed
	session.ErrorMessage = reason
	session.UpdatedAt = time.Now()
	return true, nil
}

// MarkFailed 条件迁移 pending/merging → failed。
func (r *fakeUploadRepo) MarkFailed(_ context.Context, uploadID, reason string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, exists := r.sessions[uploadID]
	if !exists || (session.Status != domupload.StatusPending && session.Status != domupload.StatusMerging) {
		return false, nil
	}
	session.Status = domupload.StatusFailed
	session.ErrorMessage = reason
	session.UpdatedAt = time.Now()
	return true, nil
}

// MarkCanceled 条件迁移 pending/merging → canceled。
func (r *fakeUploadRepo) MarkCanceled(_ context.Context, uploadID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, exists := r.sessions[uploadID]
	if !exists || (session.Status != domupload.StatusPending && session.Status != domupload.StatusMerging) {
		return false, nil
	}
	session.Status = domupload.StatusCanceled
	session.UpdatedAt = time.Now()
	return true, nil
}

// UpdateSHA256IfEmpty 仅在为空时回填期望哈希。
func (r *fakeUploadRepo) UpdateSHA256IfEmpty(_ context.Context, uploadID, sha256 string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, exists := r.sessions[uploadID]
	if exists && session.SHA256 == "" {
		session.SHA256 = sha256
	}
	return nil
}

// UpdateCoverURL 写入封面地址。
func (r *fakeUploadRepo) UpdateCoverURL(_ context.Context, uploadID, coverURL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session, exists := r.sessions[uploadID]; exists {
		session.CoverURL = coverURL
	}
	return nil
}

// UpsertPart 幂等写入分片并返回已收分片数。
func (r *fakeUploadRepo) UpsertPart(_ context.Context, uploadID string, partNumber int, sizeBytes int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.parts[uploadID] == nil {
		r.parts[uploadID] = make(map[int]int64)
	}
	r.parts[uploadID][partNumber] = sizeBytes
	return len(r.parts[uploadID]), nil
}

// ListPartNumbers 返回升序分片号。
func (r *fakeUploadRepo) ListPartNumbers(_ context.Context, uploadID string) ([]int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	numbers := make([]int, 0, len(r.parts[uploadID]))
	for number := range r.parts[uploadID] {
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)
	return numbers, nil
}

// ActiveUploadIDs 返回 pending/merging 的会话 ID。
func (r *fakeUploadRepo) ActiveUploadIDs(_ context.Context) (map[string]struct{}, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	active := make(map[string]struct{})
	for id, session := range r.sessions {
		if session.Status == domupload.StatusPending || session.Status == domupload.StatusMerging {
			active[id] = struct{}{}
		}
	}
	return active, nil
}

// DeleteTerminalBefore 删除早于 before 的终态会话与分片。
func (r *fakeUploadRepo) DeleteTerminalBefore(_ context.Context, before time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, session := range r.sessions {
		if !session.Status.Terminal() || !session.UpdatedAt.Before(before) {
			continue
		}
		delete(r.sessions, id)
		delete(r.parts, id)
	}
	return nil
}

// setStatus 直接改写会话状态，用于构造 CAS 竞态前提。
func (r *fakeUploadRepo) setStatus(uploadID string, status domupload.Status, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, exists := r.sessions[uploadID]
	if !exists {
		return
	}
	session.Status = status
	session.ErrorMessage = message
	session.UpdatedAt = time.Now()
}

// age 把会话的 updated_at 提前，用于惰性过期与清扫用例。
func (r *fakeUploadRepo) age(uploadID string, duration time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session, exists := r.sessions[uploadID]; exists {
		session.UpdatedAt = time.Now().Add(-duration)
		session.CreatedAt = session.UpdatedAt
	}
}

// statusOf 返回会话当前状态。
func (r *fakeUploadRepo) statusOf(uploadID string) domupload.Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session, exists := r.sessions[uploadID]; exists {
		return session.Status
	}
	return domupload.Status(-1)
}

// countProber 记录探测调用次数，用于断言 complete 幂等不重复跑内容校验。
type countProber struct {
	info       *media.VideoInfo
	probeErr   error
	coverErr   error
	probeCalls int
}

// ProbeVideo 计数并返回预设结果。
func (p *countProber) ProbeVideo(_ context.Context, _ string) (*media.VideoInfo, error) {
	p.probeCalls++
	return p.info, p.probeErr
}

// ExtractCover 返回预设抽帧结果。
func (p *countProber) ExtractCover(_ context.Context, _, _ string) error {
	return p.coverErr
}

// newMultipartTestHandler 构造带内存仓储与小分片的处理器。
func newMultipartTestHandler(t *testing.T, repo domupload.Repository, chunkSize int64) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	handler := New(dir, WithMultipartRepository(repo), WithChunkSize(chunkSize))
	handler.prober = &countProber{probeErr: media.ErrUnavailable, coverErr: media.ErrUnavailable}
	return handler, dir
}

// newMultipartTestEngine 注册五个分片端点，并注入固定登录用户。
func newMultipartTestEngine(t *testing.T, handler *Handler, userID int64) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(middleware.ContextUserIDKey, userID)
		c.Next()
	})
	engine.POST("/api/uploads/multipart", handler.InitMultipart)
	engine.PUT("/api/uploads/multipart/:uploadId/parts/:partNumber", handler.UploadPart)
	engine.GET("/api/uploads/multipart/:uploadId", handler.GetMultipart)
	engine.POST("/api/uploads/multipart/:uploadId/complete", handler.CompleteMultipart)
	engine.DELETE("/api/uploads/multipart/:uploadId", handler.CancelMultipart)
	return engine
}

// doMultipartJSON 发起 JSON 请求。
func doMultipartJSON(t *testing.T, engine *gin.Engine, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		payload = encoded
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(payload))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

// doMultipartPart 发起原始二进制分片请求；chunked 为 true 时清空 ContentLength。
func doMultipartPart(t *testing.T, engine *gin.Engine, uploadID string, partNumber int, content []byte, chunked bool) *httptest.ResponseRecorder {
	t.Helper()
	target := fmt.Sprintf("/api/uploads/multipart/%s/parts/%d", uploadID, partNumber)
	req := httptest.NewRequest(http.MethodPut, target, bytes.NewReader(content))
	req.Header.Set("Content-Type", "application/octet-stream")
	if chunked {
		req.ContentLength = -1
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

// decodeMultipartEnvelope 解析统一信封，返回状态码、message 与 data。
func decodeMultipartEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) (int, string, map[string]any) {
	t.Helper()
	var resp struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应非合法 JSON: %v, body=%s", err, recorder.Body.String())
	}
	return recorder.Code, resp.Message, resp.Data
}

// startMultipartSession 发起 init 并断言 201，返回响应 data。
func startMultipartSession(t *testing.T, engine *gin.Engine, filename string, size int64) map[string]any {
	t.Helper()
	recorder := doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart", map[string]any{
		"filename": filename,
		"kind":     "video",
		"size":     size,
	})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("init 期望 201，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	_, _, data := decodeMultipartEnvelope(t, recorder)
	return data
}

// uploadAllParts 按 chunkSize 切分内容并顺序上传。
func uploadAllParts(t *testing.T, engine *gin.Engine, uploadID string, content []byte, chunkSize int) {
	t.Helper()
	for start := 0; start < len(content); start += chunkSize {
		end := start + chunkSize
		if end > len(content) {
			end = len(content)
		}
		partNumber := start/chunkSize + 1
		recorder := doMultipartPart(t, engine, uploadID, partNumber, content[start:end], false)
		if recorder.Code != http.StatusOK {
			t.Fatalf("上传分片 %d 期望 200，实际 %d，body=%s", partNumber, recorder.Code, recorder.Body.String())
		}
	}
}

// TestMultipartFlowSupportsOutOfOrderDuplicateAndResume 覆盖正常流程、乱序、重复分片与跨实例续传。
func TestMultipartFlowSupportsOutOfOrderDuplicateAndResume(t *testing.T) {
	repo := newFakeUploadRepo()
	handler, dir := newMultipartTestHandler(t, repo, 4)
	content := []byte("abcdefghij") // 10 字节，chunk=4 → 3 片

	init := startMultipartSession(t, newMultipartTestEngine(t, handler, 1), "clip.mp4", int64(len(content)))
	uploadID, _ := init["upload_id"].(string)
	if uploadID == "" {
		t.Fatalf("init 未返回 upload_id: %v", init)
	}
	if init["total_parts"] != float64(3) || init["chunk_size"] != float64(4) {
		t.Fatalf("分片参数不符合预期: %v", init)
	}

	engine := newMultipartTestEngine(t, handler, 1)
	// 乱序：先末片，再首片；重复上传首片只覆盖不重复计数。
	if recorder := doMultipartPart(t, engine, uploadID, 3, content[8:10], false); recorder.Code != http.StatusOK {
		t.Fatalf("末片上传期望 200，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	first := doMultipartPart(t, engine, uploadID, 1, content[0:4], false)
	if first.Code != http.StatusOK {
		t.Fatalf("首片上传期望 200，实际 %d", first.Code)
	}
	_, _, firstData := decodeMultipartEnvelope(t, first)
	if firstData["received_parts"] != float64(2) {
		t.Fatalf("received_parts 期望 2，实际 %v", firstData["received_parts"])
	}
	duplicate := doMultipartPart(t, engine, uploadID, 1, content[0:4], false)
	_, _, duplicateData := decodeMultipartEnvelope(t, duplicate)
	if duplicateData["received_parts"] != float64(2) {
		t.Fatalf("重复分片不应重复计数，实际 %v", duplicateData["received_parts"])
	}

	// 换一个 Handler 实例（同一仓储与目录）验证断点续传。
	resumed := New(dir, WithMultipartRepository(repo), WithChunkSize(4))
	resumed.prober = &countProber{probeErr: media.ErrUnavailable}
	resumeEngine := newMultipartTestEngine(t, resumed, 1)
	stateRecorder := doMultipartJSON(t, resumeEngine, http.MethodGet, "/api/uploads/multipart/"+uploadID, nil)
	if stateRecorder.Code != http.StatusOK {
		t.Fatalf("GET 期望 200，实际 %d", stateRecorder.Code)
	}
	_, _, state := decodeMultipartEnvelope(t, stateRecorder)
	parts, _ := state["uploaded_parts"].([]any)
	if len(parts) != 2 || parts[0] != float64(1) || parts[1] != float64(3) {
		t.Fatalf("已传分片应为 [1,3] 且升序，实际 %v", state["uploaded_parts"])
	}
	if recorder := doMultipartPart(t, resumeEngine, uploadID, 2, content[4:8], false); recorder.Code != http.StatusOK {
		t.Fatalf("补传分片 2 期望 200，实际 %d", recorder.Code)
	}

	complete := doMultipartJSON(t, resumeEngine, http.MethodPost, "/api/uploads/multipart/"+uploadID+"/complete", nil)
	if complete.Code != http.StatusCreated {
		t.Fatalf("complete 期望 201，实际 %d，body=%s", complete.Code, complete.Body.String())
	}
	_, _, result := decodeMultipartEnvelope(t, complete)
	url, _ := result["url"].(string)
	if !strings.HasPrefix(url, "/uploads/video/") {
		t.Fatalf("返回 URL 形态不符: %v", result)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "video", strings.TrimPrefix(url, "/uploads/video/")))
	if err != nil {
		t.Fatalf("读取成品文件失败: %v", err)
	}
	if !bytes.Equal(stored, content) {
		t.Fatalf("合并结果与原文不一致: %q", stored)
	}
	if _, err := os.Stat(handler.tempDir(uploadID)); !os.IsNotExist(err) {
		t.Fatalf("完成后临时目录应被清理，实际 err=%v", err)
	}
}

// TestCompleteIsIdempotentAndSkipsProbe 验证重复 complete 返回 200 与同一响应体，
// 且不重复 rename、不重复执行内容校验。
func TestCompleteIsIdempotentAndSkipsProbe(t *testing.T) {
	repo := newFakeUploadRepo()
	handler, _ := newMultipartTestHandler(t, repo, 1024)
	prober := &countProber{info: &media.VideoInfo{CodecName: "h264", Width: 1280, Height: 720, DurationMS: 30_000}}
	handler.prober = prober
	engine := newMultipartTestEngine(t, handler, 1)

	content := bytes.Repeat([]byte("x"), 1500)
	init := startMultipartSession(t, engine, "clip.mp4", int64(len(content)))
	uploadID, _ := init["upload_id"].(string)
	uploadAllParts(t, engine, uploadID, content, 1024)

	first := doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+uploadID+"/complete", nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("首次 complete 期望 201，实际 %d，body=%s", first.Code, first.Body.String())
	}
	second := doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+uploadID+"/complete", nil)
	if second.Code != http.StatusOK {
		t.Fatalf("重复 complete 期望 200，实际 %d，body=%s", second.Code, second.Body.String())
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("两次响应体应完全一致:\n first=%s\nsecond=%s", first.Body.String(), second.Body.String())
	}
	if prober.probeCalls != 1 {
		t.Fatalf("内容校验只应执行一次，实际 %d 次", prober.probeCalls)
	}
}

// TestCompleteSplitsCASOutcomes 覆盖 CAS 失败后的重读分流：merging / terminal / completed。
func TestCompleteSplitsCASOutcomes(t *testing.T) {
	repo := newFakeUploadRepo()
	handler, _ := newMultipartTestHandler(t, repo, 8)
	engine := newMultipartTestEngine(t, handler, 1)
	content := []byte("0123456789")

	// merging：另一个 complete 正在合并。
	init := startMultipartSession(t, engine, "merging.mp4", int64(len(content)))
	mergingID, _ := init["upload_id"].(string)
	uploadAllParts(t, engine, mergingID, content, 8)
	repo.setStatus(mergingID, domupload.StatusMerging, "")
	recorder := doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+mergingID+"/complete", nil)
	code, _, data := decodeMultipartEnvelope(t, recorder)
	if code != http.StatusConflict || data["reason"] != reasonUploadMerging {
		t.Fatalf("merging 应返回 409+%s，实际 %d %v", reasonUploadMerging, code, data)
	}

	// failed：终态分流。
	init = startMultipartSession(t, engine, "failed.mp4", int64(len(content)))
	failedID, _ := init["upload_id"].(string)
	uploadAllParts(t, engine, failedID, content, 8)
	repo.setStatus(failedID, domupload.StatusFailed, "内容非法")
	recorder = doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+failedID+"/complete", nil)
	code, _, data = decodeMultipartEnvelope(t, recorder)
	if code != http.StatusConflict || data["reason"] != reasonTerminalState {
		t.Fatalf("failed 应返回 409+%s，实际 %d %v", reasonTerminalState, code, data)
	}

	// completed：先正常完成，再把会话强制改回 completed 后再 complete 幂等返回。
	init = startMultipartSession(t, engine, "done.mp4", int64(len(content)))
	doneID, _ := init["upload_id"].(string)
	uploadAllParts(t, engine, doneID, content, 8)
	if recorder = doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+doneID+"/complete", nil); recorder.Code != http.StatusCreated {
		t.Fatalf("正常 complete 期望 201，实际 %d", recorder.Code)
	}
	repo.setStatus(doneID, domupload.StatusCompleted, "")
	recorder = doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+doneID+"/complete", nil)
	code, _, _ = decodeMultipartEnvelope(t, recorder)
	if code != http.StatusOK {
		t.Fatalf("completed 会话重复 complete 期望 200，实际 %d，body=%s", code, recorder.Body.String())
	}
}

// TestCompleteIncompletePartsRollsBackToPending 验证缺片 409 后会回滚到 pending 以便补传。
func TestCompleteIncompletePartsRollsBackToPending(t *testing.T) {
	repo := newFakeUploadRepo()
	handler, dir := newMultipartTestHandler(t, repo, 4)
	engine := newMultipartTestEngine(t, handler, 1)
	content := []byte("abcdefghij")

	init := startMultipartSession(t, engine, "clip.mp4", int64(len(content)))
	uploadID, _ := init["upload_id"].(string)
	if recorder := doMultipartPart(t, engine, uploadID, 1, content[0:4], false); recorder.Code != http.StatusOK {
		t.Fatalf("分片 1 上传失败: %d", recorder.Code)
	}

	recorder := doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+uploadID+"/complete", nil)
	code, _, data := decodeMultipartEnvelope(t, recorder)
	if code != http.StatusConflict || data["reason"] != reasonPartsIncomplete {
		t.Fatalf("缺片应返回 409+%s，实际 %d %v", reasonPartsIncomplete, code, data)
	}
	if status := repo.statusOf(uploadID); status != domupload.StatusPending {
		t.Fatalf("缺片后应回滚为 pending，实际 %v", status)
	}

	// DB 行存在但文件缺失同样按缺片处理（文档 §5.2）。
	other := startMultipartSession(t, engine, "lost.mp4", int64(len(content)))
	lostID, _ := other["upload_id"].(string)
	uploadAllParts(t, engine, lostID, content, 4)
	if err := os.Remove(filepath.Join(dir, ".upload-tmp", lostID, partFileName(2))); err != nil {
		t.Fatalf("删除分片文件失败: %v", err)
	}
	recorder = doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+lostID+"/complete", nil)
	code, _, data = decodeMultipartEnvelope(t, recorder)
	if code != http.StatusConflict || data["reason"] != reasonPartsIncomplete {
		t.Fatalf("分片文件缺失应按缺片返回，实际 %d %v", code, data)
	}

	// 补齐后可以正常完成。
	if recorder = doMultipartPart(t, engine, uploadID, 2, content[4:8], false); recorder.Code != http.StatusOK {
		t.Fatalf("补传分片 2 失败: %d", recorder.Code)
	}
	if recorder = doMultipartPart(t, engine, uploadID, 3, content[8:10], false); recorder.Code != http.StatusOK {
		t.Fatalf("补传分片 3 失败: %d", recorder.Code)
	}
	if recorder = doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+uploadID+"/complete", nil); recorder.Code != http.StatusCreated {
		t.Fatalf("补齐后 complete 期望 201，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestCompleteHashMismatchMarksFailed 验证 SHA-256 不符返回 400、会话置 failed、临时目录被删。
func TestCompleteHashMismatchMarksFailed(t *testing.T) {
	repo := newFakeUploadRepo()
	handler, _ := newMultipartTestHandler(t, repo, 4)
	engine := newMultipartTestEngine(t, handler, 1)
	content := []byte("abcdefgh")

	recorder := doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart", map[string]any{
		"filename": "clip.mp4",
		"kind":     "video",
		"size":     len(content),
		"sha256":   strings.Repeat("0", 64),
	})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("init 期望 201，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	_, _, init := decodeMultipartEnvelope(t, recorder)
	uploadID, _ := init["upload_id"].(string)
	uploadAllParts(t, engine, uploadID, content, 4)

	complete := doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+uploadID+"/complete", nil)
	if complete.Code != http.StatusBadRequest {
		t.Fatalf("哈希不符期望 400，实际 %d，body=%s", complete.Code, complete.Body.String())
	}
	if status := repo.statusOf(uploadID); status != domupload.StatusFailed {
		t.Fatalf("哈希不符后会话应置 failed，实际 %v", status)
	}
	if _, err := os.Stat(handler.tempDir(uploadID)); !os.IsNotExist(err) {
		t.Fatalf("失败后临时目录应被删除，实际 err=%v", err)
	}
}

// TestMultipartInitValidation 覆盖 size<=0、非法扩展名、超限与复用/并发上限。
func TestMultipartInitValidation(t *testing.T) {
	repo := newFakeUploadRepo()
	handler, dir := newMultipartTestHandler(t, repo, 4)
	handler.limits.videoBytes = 8
	engine := newMultipartTestEngine(t, handler, 1)

	recorder := doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart", map[string]any{
		"filename": "empty.mp4", "kind": "video", "size": 0,
	})
	code, message, _ := decodeMultipartEnvelope(t, recorder)
	if code != http.StatusBadRequest || !strings.Contains(message, "素材文件为空") {
		t.Fatalf("size=0 应返回 400 与空文件提示，实际 %d %q", code, message)
	}

	recorder = doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart", map[string]any{
		"filename": "evil.exe", "kind": "video", "size": 4,
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法扩展名期望 400，实际 %d", recorder.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, ".upload-tmp")); !os.IsNotExist(err) {
		t.Fatalf("校验失败不应产生临时目录，实际 err=%v", err)
	}

	recorder = doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart", map[string]any{
		"filename": "big.mp4", "kind": "video", "size": 9,
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("超限期望 400，实际 %d", recorder.Code)
	}

	// 复用：同文件名同大小命中 pending 会话。
	first := startMultipartSession(t, engine, "reuse.mp4", 8)
	reuseID, _ := first["upload_id"].(string)
	second := doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart", map[string]any{
		"filename": "reuse.mp4", "kind": "video", "size": 8,
	})
	if second.Code != http.StatusOK {
		t.Fatalf("复用期望 200，实际 %d，body=%s", second.Code, second.Body.String())
	}
	_, _, reused := decodeMultipartEnvelope(t, second)
	if reused["upload_id"] != reuseID {
		t.Fatalf("复用应返回同一会话，期望 %s，实际 %v", reuseID, reused["upload_id"])
	}

	// 活跃会话达到 3 个上限后返回 429。
	startMultipartSession(t, engine, "a.mp4", 8)
	startMultipartSession(t, engine, "b.mp4", 8)
	recorder = doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart", map[string]any{
		"filename": "c.mp4", "kind": "video", "size": 8,
	})
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("活跃会话达到上限期望 429，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestUploadPartRejectsOversizedAndInvalid 覆盖 413（含 chunked）、分片过短、越界、非本人与终态。
func TestUploadPartRejectsOversizedAndInvalid(t *testing.T) {
	repo := newFakeUploadRepo()
	handler, dir := newMultipartTestHandler(t, repo, 4)
	engine := newMultipartTestEngine(t, handler, 1)
	content := []byte("abcdefgh")

	init := startMultipartSession(t, engine, "clip.mp4", int64(len(content)))
	uploadID, _ := init["upload_id"].(string)

	// Content-Length 预检命中：超限 413 且不保留 .tmp。
	recorder := doMultipartPart(t, engine, uploadID, 1, bytes.Repeat([]byte("z"), 10), false)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限分片期望 413，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	assertNoPartFiles(t, dir, uploadID)

	// 无 Content-Length 的 chunked 请求同样被 MaxBytesReader 拦截。
	recorder = doMultipartPart(t, engine, uploadID, 1, bytes.Repeat([]byte("z"), 10), true)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked 超限期望 413，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	assertNoPartFiles(t, dir, uploadID)

	// 过短分片返回 400。
	recorder = doMultipartPart(t, engine, uploadID, 1, []byte("abc"), false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("过短分片期望 400，实际 %d", recorder.Code)
	}

	// 越界分片号返回 400。
	recorder = doMultipartPart(t, engine, uploadID, 5, content[0:4], false)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("越界分片号期望 400，实际 %d", recorder.Code)
	}

	// 非本人返回 403。
	otherEngine := newMultipartTestEngine(t, handler, 2)
	recorder = doMultipartPart(t, otherEngine, uploadID, 1, content[0:4], false)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("非本人期望 403，实际 %d", recorder.Code)
	}

	// 会话被取消后 PUT 返回 409。
	repo.setStatus(uploadID, domupload.StatusCanceled, "")
	recorder = doMultipartPart(t, engine, uploadID, 1, content[0:4], false)
	code, _, data := decodeMultipartEnvelope(t, recorder)
	if code != http.StatusConflict || data["reason"] != reasonTerminalState {
		t.Fatalf("取消后 PUT 期望 409+%s，实际 %d %v", reasonTerminalState, code, data)
	}
}

// assertNoPartFiles 断言会话目录下没有任何分片文件残留。
func assertNoPartFiles(t *testing.T, uploadDir, uploadID string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(uploadDir, ".upload-tmp", uploadID))
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("读取临时目录失败: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".part") || strings.HasSuffix(entry.Name(), ".part.tmp") {
			t.Fatalf("超限分片不应保留文件，实际存在 %s", entry.Name())
		}
	}
}

// TestReadPathLazyExpiration 验证 GET / complete 读到超 24h 的会话时直接 410 并置 expired。
func TestReadPathLazyExpiration(t *testing.T) {
	repo := newFakeUploadRepo()
	handler, _ := newMultipartTestHandler(t, repo, 4)
	engine := newMultipartTestEngine(t, handler, 1)
	content := []byte("abcdefgh")

	for _, action := range []string{"get", "complete"} {
		init := startMultipartSession(t, engine, action+".mp4", int64(len(content)))
		uploadID, _ := init["upload_id"].(string)
		uploadAllParts(t, engine, uploadID, content, 4)
		repo.age(uploadID, 25*time.Hour)

		var recorder *httptest.ResponseRecorder
		if action == "get" {
			recorder = doMultipartJSON(t, engine, http.MethodGet, "/api/uploads/multipart/"+uploadID, nil)
		} else {
			recorder = doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+uploadID+"/complete", nil)
		}
		if recorder.Code != http.StatusGone {
			t.Fatalf("%s 超期会话期望 410，实际 %d，body=%s", action, recorder.Code, recorder.Body.String())
		}
		if status := repo.statusOf(uploadID); status != domupload.StatusExpired {
			t.Fatalf("%s 应把会话置 expired，实际 %v", action, status)
		}
	}
}

// TestCancelVersusMerge 覆盖两条竞争路径：合并先完成时取消 409 且成品保留；
// 取消先赢时合并侧不返回 500。
func TestCancelVersusMerge(t *testing.T) {
	repo := newFakeUploadRepo()
	handler, dir := newMultipartTestHandler(t, repo, 4)
	engine := newMultipartTestEngine(t, handler, 1)
	content := []byte("abcdefgh")

	// 合并先赢。
	init := startMultipartSession(t, engine, "win.mp4", int64(len(content)))
	uploadID, _ := init["upload_id"].(string)
	uploadAllParts(t, engine, uploadID, content, 4)
	complete := doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+uploadID+"/complete", nil)
	if complete.Code != http.StatusCreated {
		t.Fatalf("complete 期望 201，实际 %d", complete.Code)
	}
	_, _, result := decodeMultipartEnvelope(t, complete)
	url, _ := result["url"].(string)
	dest := filepath.Join(dir, "video", strings.TrimPrefix(url, "/uploads/video/"))

	cancel := doMultipartJSON(t, engine, http.MethodDelete, "/api/uploads/multipart/"+uploadID, nil)
	if cancel.Code != http.StatusConflict {
		t.Fatalf("已完成会话取消期望 409，实际 %d", cancel.Code)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("已完成成品不应被删除: %v", err)
	}

	// 取消先赢：合并侧在 CAS 成功后遇到目录被删，只能静默中止，不能 500。
	init = startMultipartSession(t, engine, "lose.mp4", int64(len(content)))
	raceID, _ := init["upload_id"].(string)
	uploadAllParts(t, engine, raceID, content, 4)
	repo.onMarkMerging = func() {
		// 模拟取消先赢：状态已置 canceled，且临时目录已被清理。
		repo.setStatus(raceID, domupload.StatusCanceled, "")
		_ = os.RemoveAll(handler.tempDir(raceID))
	}
	race := doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+raceID+"/complete", nil)
	repo.onMarkMerging = nil
	if race.Code == http.StatusInternalServerError {
		t.Fatalf("取消竞争不应返回 500，body=%s", race.Body.String())
	}
	if race.Code != http.StatusConflict {
		t.Fatalf("取消竞争期望 409，实际 %d，body=%s", race.Code, race.Body.String())
	}
}

// TestMergePartsMissingFile 直接验证合并函数遇到 ENOENT 返回可识别的错误。
func TestMergePartsMissingFile(t *testing.T) {
	handler, _ := newMultipartTestHandler(t, newFakeUploadRepo(), 4)
	session := &domupload.Session{UploadID: "missing", ChunkSize: 4, TotalParts: 2, SizeBytes: 8}
	dir := t.TempDir()
	if _, _, err := handler.mergeParts(dir, session); !os.IsNotExist(err) {
		t.Fatalf("分片缺失应返回 ENOENT，实际 %v", err)
	}
}

// TestSweepReclaimsStaleSessionsAndOrphanDirs 覆盖兜底清扫的四类对象。
func TestSweepReclaimsStaleSessionsAndOrphanDirs(t *testing.T) {
	repo := newFakeUploadRepo()
	handler, dir := newMultipartTestHandler(t, repo, 4)
	ctx := context.Background()
	now := time.Now()

	// 1. 超期 pending：目录被删、状态置 expired。
	pending := &domupload.Session{UploadID: "stale-pending", UserID: 1, Kind: "video", Filename: "p.mp4",
		SizeBytes: 4, ChunkSize: 4, TotalParts: 1, Status: domupload.StatusPending}
	if err := repo.CreateSession(ctx, pending); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	writeTempPart(t, handler, pending.UploadID)
	repo.age(pending.UploadID, 25*time.Hour)

	// 2. 卡死 merging：目录被删、状态置 failed。
	merging := &domupload.Session{UploadID: "stale-merging", UserID: 1, Kind: "video", Filename: "m.mp4",
		SizeBytes: 4, ChunkSize: 4, TotalParts: 1, Status: domupload.StatusMerging}
	if err := repo.CreateSession(ctx, merging); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	writeTempPart(t, handler, merging.UploadID)
	repo.age(merging.UploadID, 31*time.Minute)

	// 3. 无会话孤儿目录：mtime 超 24h 直接删除。
	orphanDir := filepath.Join(dir, ".upload-tmp", "orphan")
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatalf("创建孤儿目录失败: %v", err)
	}
	old := now.Add(-25 * time.Hour)
	if err := os.Chtimes(orphanDir, old, old); err != nil {
		t.Fatalf("修改目录时间失败: %v", err)
	}

	// 4. failed 会话的残留目录同样被回收。
	failed := &domupload.Session{UploadID: "failed-residual", UserID: 1, Kind: "video", Filename: "f.mp4",
		SizeBytes: 4, ChunkSize: 4, TotalParts: 1, Status: domupload.StatusFailed}
	if err := repo.CreateSession(ctx, failed); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	writeTempPart(t, handler, failed.UploadID)
	residual := handler.tempDir(failed.UploadID)
	if err := os.Chtimes(residual, old, old); err != nil {
		t.Fatalf("修改目录时间失败: %v", err)
	}

	handler.sweep(ctx, now)

	if status := repo.statusOf(pending.UploadID); status != domupload.StatusExpired {
		t.Fatalf("超期 pending 应置 expired，实际 %v", status)
	}
	if status := repo.statusOf(merging.UploadID); status != domupload.StatusFailed {
		t.Fatalf("卡死 merging 应置 failed，实际 %v", status)
	}
	for _, gone := range []string{handler.tempDir(pending.UploadID), handler.tempDir(merging.UploadID), orphanDir, residual} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("目录应被回收: %s, err=%v", gone, err)
		}
	}
}

// writeTempPart 在会话临时目录下写入一个分片文件。
func writeTempPart(t *testing.T, handler *Handler, uploadID string) {
	t.Helper()
	dir := handler.tempDir(uploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, partFileName(1)), []byte("abcd"), 0o644); err != nil {
		t.Fatalf("写入分片失败: %v", err)
	}
}

// TestCompleteContentValidationRejectsInvalidVideo 验证合并产物的内容校验失败时返回 400、
// 会话置 failed，且成品目录不产生文件（校验发生在 rename 之前）。
func TestCompleteContentValidationRejectsInvalidVideo(t *testing.T) {
	repo := newFakeUploadRepo()
	handler, dir := newMultipartTestHandler(t, repo, 4)
	handler.prober = &countProber{info: &media.VideoInfo{
		CodecName: "h264", Width: 1920, Height: 1080, DurationMS: MaxVideoDurationMS + 1,
	}}
	engine := newMultipartTestEngine(t, handler, 1)
	content := []byte("abcdefgh")

	init := startMultipartSession(t, engine, "long.mp4", int64(len(content)))
	uploadID, _ := init["upload_id"].(string)
	uploadAllParts(t, engine, uploadID, content, 4)

	recorder := doMultipartJSON(t, engine, http.MethodPost, "/api/uploads/multipart/"+uploadID+"/complete", nil)
	code, message, _ := decodeMultipartEnvelope(t, recorder)
	if code != http.StatusBadRequest || !strings.Contains(message, "时长超过上限") {
		t.Fatalf("超时长视频期望 400 与时长提示，实际 %d %q", code, message)
	}
	if status := repo.statusOf(uploadID); status != domupload.StatusFailed {
		t.Fatalf("内容校验失败后会话应置 failed，实际 %v", status)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "video")); err == nil && len(entries) > 0 {
		t.Fatalf("校验失败不应进入成品目录，实际存在 %d 个文件", len(entries))
	}
}

// TestCancelIsIdempotent 验证取消的幂等与终态分流。
func TestCancelIsIdempotent(t *testing.T) {
	repo := newFakeUploadRepo()
	handler, _ := newMultipartTestHandler(t, repo, 4)
	engine := newMultipartTestEngine(t, handler, 1)

	init := startMultipartSession(t, engine, "cancel.mp4", 8)
	uploadID, _ := init["upload_id"].(string)
	if recorder := doMultipartJSON(t, engine, http.MethodDelete, "/api/uploads/multipart/"+uploadID, nil); recorder.Code != http.StatusOK {
		t.Fatalf("首次取消期望 200，实际 %d", recorder.Code)
	}
	if recorder := doMultipartJSON(t, engine, http.MethodDelete, "/api/uploads/multipart/"+uploadID, nil); recorder.Code != http.StatusOK {
		t.Fatalf("重复取消期望 200，实际 %d", recorder.Code)
	}
	if recorder := doMultipartJSON(t, engine, http.MethodDelete, "/api/uploads/multipart/not-exist", nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在会话期望 404，实际 %d", recorder.Code)
	}
}
