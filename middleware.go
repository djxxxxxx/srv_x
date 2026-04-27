package main

import (
	"context"
	"log"
	"net/http"
	"time"
)

// contextKey 自定义上下文键类型，避免与其他包冲突
type contextKey string

const (
	ctxUsername contextKey = "username"
	ctxUserID   contextKey = "userID"
	ctxRole     contextKey = "role"
)

// authMiddleware 认证中间件
func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		username, ok := sessions[cookie.Value]
		if !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		// 获取用户信息
		user, err := GetUserByUsername(username)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		// 将用户信息存入上下文
		ctx := r.Context()
		ctx = context.WithValue(ctx, ctxUsername, username)
		ctx = context.WithValue(ctx, ctxUserID, user.ID)
		ctx = context.WithValue(ctx, ctxRole, user.Role)
		next(w, r.WithContext(ctx))
	}
}

// adminMiddleware 管理员权限中间件
func adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		role := r.Context().Value(ctxRole)
		if role != "admin" {
			http.Error(w, "无权限访问", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// loggingMiddleware 请求日志中间件
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		duration := time.Since(start)
		if duration > 100*time.Millisecond {
			log.Printf("[%s] %s %s (%.2fms)", r.Method, r.URL.Path, r.RemoteAddr, float64(duration.Nanoseconds())/1e6)
		}
	})
}

// getContextUsername 从上下文中安全获取用户名
func getContextUsername(r *http.Request) string {
	v, _ := r.Context().Value(ctxUsername).(string)
	return v
}

// getContextUserID 从上下文中安全获取用户ID
func getContextUserID(r *http.Request) int64 {
	v, _ := r.Context().Value(ctxUserID).(int64)
	return v
}

// getContextRole 从上下文中安全获取角色
func getContextRole(r *http.Request) string {
	v, _ := r.Context().Value(ctxRole).(string)
	return v
}
