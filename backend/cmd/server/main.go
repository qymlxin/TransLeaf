package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"translation-assistant/internal/api"
	"translation-assistant/internal/config"
	"translation-assistant/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	st, err := store.New(cfg.DBPath)
	if err != nil {
		log.Fatalf("初始化 SQLite 数据库失败: %v", err)
	}
	defer st.Close()

	srv := api.NewServer(cfg, st)
	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: srv.Router(),
	}

	go func() {
		log.Printf("TranslationAssistant Go 后端已启动: http://127.0.0.1%s", cfg.Addr)
		if cfg.FontPath != "" {
			log.Printf("已加载中文字体: %s", cfg.FontPath)
		} else {
			log.Printf("警告: 未检测到中文字体文件，请将 NotoSansSC-Regular.ttf 放入 backend/assets/fonts/")
		}
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务启动失败: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	log.Println("服务已优雅退出")
}
