package observability

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Check 描述一个就绪检查项（如配置、数据库、Telegram 接入）。
type Check struct {
	Name string
	Func func(context.Context) error
}

// NewHealthHandler 返回健康检查 HTTP handler，仅提供运行状态接口：
//   - /healthz 恒返回 200，表示进程存活；
//   - /readyz 顺序执行全部 Check：首个失败即短路（不再执行后续检查项），
//     任一失败返回 503 并在响应体中列出已发现失败项的名称与原因；
//     全部通过返回 200；空 checks 视为全部通过；
//   - 其余路径返回 404。
//
// 构造时校验检查项：Name 为空或 Func 为 nil 立即 panic（fail fast，
// 这类错误属于编程错误，应在启动时暴露而非逐请求 panic）。
func NewHealthHandler(checks []Check) http.Handler {
	for _, c := range checks {
		if c.Name == "" {
			panic("observability: health check name must not be empty")
		}
		if c.Func == nil {
			panic(fmt.Sprintf("observability: health check %q has nil Func", c.Name))
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ok\n")
		case "/readyz":
			ctx := r.Context()
			var failures []string
			for _, c := range checks {
				if err := c.Func(ctx); err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", c.Name, err))
					break
				}
			}
			if len(failures) > 0 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, strings.Join(failures, "\n")+"\n")
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ready\n")
		default:
			http.NotFound(w, r)
		}
	})
}
