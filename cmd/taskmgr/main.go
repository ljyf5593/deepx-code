package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"deepx/taskmgr"
)

func main() {
	port := "8080"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	store := taskmgr.NewTaskStore()
	handler := taskmgr.NewHandler(store)

	mux := http.NewServeMux()
	handler.Register(mux)

	// 静态文件服务(前端 SPA)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "taskmgr/static/index.html")
	})

	// 为 API 添加 CORS
	apiHandler := taskmgr.CorsMiddleware(mux)

	addr := fmt.Sprintf(":%s", port)
	log.Printf("✅ 任务管理系统已启动: http://localhost%s\n", addr)
	if err := http.ListenAndServe(addr, apiHandler); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}
