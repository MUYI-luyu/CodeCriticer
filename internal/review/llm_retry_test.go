package review

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestChatRetryOn429 验证：429 限流会重试，成功后 metrics 正确统计 token 与重试次数。
func TestChatRetryOn429(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"findings\":[]}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer srv.Close()

	l := NewLLMWithConfig(WithAPIKey("test"), WithBaseURL(srv.URL), WithReviewModel("test-model"))
	if _, err := l.Review(context.Background(), "diff"); err != nil {
		t.Fatalf("期望重试后成功，得到: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("期望 2 次尝试（1 次失败 + 1 次重试成功），得到 %d", attempts)
	}

	s := l.Metrics()["test-model"]
	if s.Calls != 1 || s.Success != 1 || s.Fail != 0 || s.Retries != 1 {
		t.Fatalf("metrics 计数错误: %+v", s)
	}
	if s.InputTokens != 10 || s.OutputTokens != 5 {
		t.Fatalf("token 统计错误: %+v", s)
	}
}

// TestChatNoRetryOn400 验证：400 属于不可重试错误，不重试但会沿降级链依次尝试各模型。
func TestChatNoRetryOn400(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer srv.Close()

	l := NewLLMWithConfig(WithAPIKey("test"), WithBaseURL(srv.URL),
		WithReviewModel("review-model"), WithReflectModel("reflect-model"), WithPlanModel("plan-model"))
	if _, err := l.Review(context.Background(), "diff"); err == nil {
		t.Fatal("期望失败，得到成功")
	}
	// 降级链依次尝试 3 个不同模型各 1 次，400 不重试
	if attempts != 3 {
		t.Fatalf("期望 3 次尝试（3 个不同模型各 1 次，不重试），得到 %d", attempts)
	}

	s := l.Metrics()["review-model"]
	if s.Calls != 1 || s.Success != 0 || s.Fail != 1 || s.Retries != 0 {
		t.Fatalf("ReviewModel metrics 错误: %+v", s)
	}
}

// TestChatFallbackDedup 验证：降级链跳过重复模型，避免 --model 统一设置时对同一模型重复调用。
func TestChatFallbackDedup(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer srv.Close()

	// 三个模型设成同一个名字（等价 --model 统一设置）
	l := NewLLMWithConfig(WithAPIKey("test"), WithBaseURL(srv.URL),
		WithReviewModel("same-model"), WithReflectModel("same-model"), WithPlanModel("same-model"))
	if _, err := l.Review(context.Background(), "diff"); err == nil {
		t.Fatal("期望失败，得到成功")
	}
	// 去重后只尝试 1 个模型
	if attempts != 1 {
		t.Fatalf("期望 1 次尝试（重复模型被去重），得到 %d", attempts)
	}
}
