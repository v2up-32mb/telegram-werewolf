// Package observability 提供结构化日志、健康检查与轻量计数器，
// 对应《技术选型》§11 的日志、健康检查与指标规格。
package observability

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
)

// 敏感字段脱敏掩码。原始值（Bot Token、Webhook Secret 等）绝不出现在日志输出中。
const redactedValue = "***"

// sensitiveHints 是敏感键的规范化提示词集合（均小写、已去分隔符）。
// 规范化后的键只要包含任一提示词即视为敏感，宁可多掩码也不错漏。
var sensitiveHints = []string{
	"token",
	"secret",
	"password",
	"authorization",
	"apikey",
	"credential",
}

// NewLogger 依据 format 创建一个结构化日志器：
//   - "text" 输出人类易读文本（开发环境）；
//   - "json" 输出 JSON（生产环境）。
//
// 返回的日志器对键名（不区分大小写、忽略 _ - 与空格）含
// token/secret/password/authorization/api-key/credential 等敏感词的字段
// 递归脱敏：覆盖顶层字段、group 嵌套字段以及 slog.Any 传入的 map/struct
// 嵌套值。room_id/game_id/phase 等业务字段原样透传。
//
// 脱敏边界：日志消息文本（msg）与自由文本值（如 error 输出）不在
// 键驱动脱敏的保证范围内，调用方不得将密钥放入消息或任意文本值中。
// 不支持的格式返回明确错误。
func NewLogger(format string, out io.Writer) (*slog.Logger, error) {
	var base slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "text":
		base = slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo})
	case "json":
		base = slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo})
	default:
		return nil, fmt.Errorf("unsupported log format %q (want text or json)", format)
	}
	return slog.New(&redactingHandler{inner: base}), nil
}

// redactingHandler 包装底层 slog.Handler，在写出前对敏感字段执行脱敏。
type redactingHandler struct {
	inner slog.Handler
}

// Enabled 透传底层 handler 的级别过滤。
func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle 对记录的全部属性递归脱敏后再交给底层 handler 输出。
func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, redactAttr(a))
		return true
	})
	redacted := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	redacted.AddAttrs(attrs...)
	return h.inner.Handle(ctx, redacted)
}

// WithAttrs 透传底层 handler 的字段注入（注入字段同样经过脱敏）。
func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &redactingHandler{inner: h.inner.WithAttrs(redactAttrs(attrs))}
}

// WithGroup 透传底层 handler 的 group 支持。
func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

// redactAttrs 对一批属性逐项脱敏。
func redactAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = redactAttr(a)
	}
	return out
}

// redactAttr 对单个属性脱敏：键含敏感词时整体掩码；
// 值则递归处理 group 与任意嵌套值。
func redactAttr(a slog.Attr) slog.Attr {
	if isSensitiveKey(a.Key) {
		return slog.String(a.Key, redactedValue)
	}
	return slog.Attr{Key: a.Key, Value: redactValue(a.Value)}
}

// redactValue 递归脱敏一个 slog 值：group 展开子属性，
// 任意值（map/struct/slice 等）经 reflect 遍历敏感键。
func redactValue(v slog.Value) slog.Value {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		return slog.GroupValue(redactAttrs(v.Group())...)
	case slog.KindAny:
		return slog.AnyValue(redactAny(v.Any()))
	default:
		return v
	}
}

// redactAny 递归遍历任意嵌套值，将敏感键对应的字符串值替换为掩码。
// 仅处理可安全重建的类型（string 键 map、struct、slice/array、指针、interface），
// 无法读取或类型不匹配的字段保持原样。
func redactAny(v any) any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String || rv.IsNil() {
			return v
		}
		out := reflect.MakeMap(rv.Type())
		iter := rv.MapRange()
		for iter.Next() {
			key, val := iter.Key(), iter.Value()
			if isSensitiveKey(key.String()) {
				masked := reflect.ValueOf(redactedValue)
				if masked.Type().AssignableTo(rv.Type().Elem()) {
					out.SetMapIndex(key, masked)
				} else {
					out.SetMapIndex(key, val)
				}
			} else {
				out.SetMapIndex(key, reflect.ValueOf(redactAny(val.Interface())))
			}
		}
		return out.Interface()
	case reflect.Struct:
		out := reflect.New(rv.Type()).Elem()
		for i := 0; i < rv.NumField(); i++ {
			field := rv.Field(i)
			if !field.CanInterface() {
				continue
			}
			if isSensitiveKey(rv.Type().Field(i).Name) {
				masked := reflect.ValueOf(redactedValue)
				if masked.Type().AssignableTo(field.Type()) {
					out.Field(i).Set(masked)
				} else {
					out.Field(i).Set(field)
				}
			} else {
				out.Field(i).Set(reflect.ValueOf(redactAny(field.Interface())))
			}
		}
		return out.Interface()
	case reflect.Slice:
		if rv.IsNil() {
			return v
		}
		out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(reflect.ValueOf(redactAny(rv.Index(i).Interface())))
		}
		return out.Interface()
	case reflect.Array:
		out := reflect.New(rv.Type()).Elem()
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(reflect.ValueOf(redactAny(rv.Index(i).Interface())))
		}
		return out.Interface()
	case reflect.Pointer:
		if rv.IsNil() {
			return v
		}
		out := reflect.New(rv.Type().Elem())
		out.Elem().Set(reflect.ValueOf(redactAny(rv.Elem().Interface())))
		return out.Interface()
	case reflect.Interface:
		if rv.IsNil() {
			return v
		}
		return redactAny(rv.Elem().Interface())
	default:
		return v
	}
}

// isSensitiveKey 判断键名是否属于敏感字段（不区分大小写、忽略分隔符）。
func isSensitiveKey(key string) bool {
	norm := strings.ToLower(key)
	norm = strings.ReplaceAll(norm, "_", "")
	norm = strings.ReplaceAll(norm, "-", "")
	norm = strings.ReplaceAll(norm, " ", "")
	for _, hint := range sensitiveHints {
		if strings.Contains(norm, hint) {
			return true
		}
	}
	return false
}
