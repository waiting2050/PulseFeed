//go:build integration

// mq_confirm_integration_test.go 验证 publisher confirm 能把 broker 的拒绝反馈给发布方：
// 投递到不存在的交换机会触发通道异常，PublishRaw 必须返回错误而不是“发出去就当真成功”。
package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	inframq "pulsefeed/internal/infra/mq"
)

func TestPublishConfirmSurfacesBrokerRejection(t *testing.T) {
	suffix := fmt.Sprintf("it%x", time.Now().UnixNano())
	cfg, bindings := newDLQTestConfig(suffix)

	client, err := inframq.New(cfg)
	if err != nil {
		t.Fatalf("初始化 RabbitMQ 客户端失败: %v", err)
	}
	defer client.Close()
	t.Cleanup(func() { cleanupDLQTopology(t, cfg, bindings) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	missingExchange := "it.dlq." + suffix + ".missing"
	if err := client.PublishRaw(ctx, missingExchange, "no.route", "evt-rejected", []byte(`{}`)); err == nil {
		t.Fatalf("投递到不存在的交换机 %q 应返回错误", missingExchange)
	} else {
		t.Logf("broker 拒绝已感知: %v", err)
	}
}
