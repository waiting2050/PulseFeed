//go:build integration

// play_count_integration_test.go 验证跨表同 event_id 的双向去重与并发累加：
// pf_play_events 唯一键拦住同事件重复写入，pf_playback_qos_reports 命中的 event_id 不再计入 play_count。
package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	domimpression "pulsefeed/internal/domain/impression"
	infraclip "pulsefeed/internal/infra/persistence/clip"
	infraimpression "pulsefeed/internal/infra/persistence/impression"
	infraplayback "pulsefeed/internal/infra/persistence/playback"
)

func TestPlayCountDedupUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	const viewerID int64 = 7001

	clipID := seedClip(t, 9001, "it-play-count", time.Now().Add(-time.Minute), 1)
	if err := testDB.Create(&infraclip.ClipStatsModel{ClipID: clipID}).Error; err != nil {
		t.Fatalf("写入计数行失败: %v", err)
	}
	repo := infraimpression.New(testDB)

	// 与 QoS 上报表同 event_id 的事件：已由 QoS 表达同一次播放，不应再累加 play_count。
	qosEventID := uniqueKey("it-qos-event")
	if err := testDB.Create(&infraplayback.QoSReportModel{
		ReporterID: viewerID,
		ClipID:     clipID,
		EventID:    qosEventID,
	}).Error; err != nil {
		t.Fatalf("写入 QoS 报表失败: %v", err)
	}

	// 5 个唯一事件，由 2 个 goroutine 各上报一遍（共 10 次调用）。
	uniqueEvents := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		uniqueEvents = append(uniqueEvents, uniqueKey(fmt.Sprintf("it-play-%d", i)))
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, eventID := range uniqueEvents {
				event, err := domimpression.NewPlayEvent(eventID, viewerID, clipID, 1200, false, true)
				if err != nil {
					errCh <- err
					return
				}
				if _, err := repo.RecordPlayEvent(ctx, event); err != nil {
					errCh <- fmt.Errorf("event %s: %w", eventID, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		t.Fatalf("并发上报失败: %v", err)
	}

	// 同一 event_id 再串行上报一次，必须幂等返回 false 且不再累加。
	first := uniqueEvents[0]
	event, err := domimpression.NewPlayEvent(first, viewerID, clipID, 1200, false, true)
	if err != nil {
		t.Fatalf("构造重复事件失败: %v", err)
	}
	created, err := repo.RecordPlayEvent(ctx, event)
	if err != nil {
		t.Fatalf("重复上报失败: %v", err)
	}
	if created {
		t.Fatalf("同一 event_id 重复上报应返回 false")
	}

	// QoS 已存在的事件首次写入播放流水，但不累加 play_count。
	qosEvent, err := domimpression.NewPlayEvent(qosEventID, viewerID, clipID, 1200, false, true)
	if err != nil {
		t.Fatalf("构造 QoS 事件失败: %v", err)
	}
	if created, err = repo.RecordPlayEvent(ctx, qosEvent); err != nil || !created {
		t.Fatalf("QoS 事件首次写入应为新事件: created=%v err=%v", created, err)
	}

	var stats infraclip.ClipStatsModel
	if err := testDB.First(&stats, "clip_id = ?", clipID).Error; err != nil {
		t.Fatalf("读取计数行失败: %v", err)
	}
	if stats.PlayCount != int64(len(uniqueEvents)) {
		t.Fatalf("play_count 去重后应为 %d，实际 %d", len(uniqueEvents), stats.PlayCount)
	}

	var eventRows int64
	if err := testDB.Model(&infraimpression.PlayEventModel{}).
		Where("clip_id = ?", clipID).Count(&eventRows).Error; err != nil {
		t.Fatalf("统计播放事件失败: %v", err)
	}
	if eventRows != int64(len(uniqueEvents))+1 {
		t.Fatalf("播放流水应为 %d 行（5 唯一 + 1 QoS 事件），实际 %d", len(uniqueEvents)+1, eventRows)
	}

	var impressions []infraimpression.ImpressionModel
	if err := testDB.Where("viewer_id = ? AND clip_id = ?", viewerID, clipID).Find(&impressions).Error; err != nil {
		t.Fatalf("读取展示聚合失败: %v", err)
	}
	if len(impressions) != 1 || impressions[0].PlayCount != int64(len(uniqueEvents))+1 {
		t.Fatalf("展示聚合应只有一行且 play_count=%d，实际 %+v", len(uniqueEvents)+1, impressions)
	}

	t.Logf("并发调用=10，唯一事件=%d，QoS 重合事件=1，最终 play_count=%d，播放流水=%d，展示聚合 play_count=%d",
		len(uniqueEvents), stats.PlayCount, eventRows, impressions[0].PlayCount)
}
