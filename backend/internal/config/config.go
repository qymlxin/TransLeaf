package config

import (
	"os"
	"path/filepath"
)

// Config 保存后端运行所需路径与网络配置
type Config struct {
	Addr          string
	DataDir       string
	UploadsDir    string
	CleanBgDir    string
	ThumbnailsDir string
	ExportsDir    string
	DBPath        string
	FontPath      string
	MaxUploadSize int64
}

// Load 加载默认与环境变量配置，并确保运行时目录存在
func Load() (*Config, error) {
	addr := os.Getenv("SERVER_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Addr:          addr,
		DataDir:       absData,
		UploadsDir:    filepath.Join(absData, "uploads"),
		CleanBgDir:    filepath.Join(absData, "clean_bg"),
		ThumbnailsDir: filepath.Join(absData, "thumbnails"),
		ExportsDir:    filepath.Join(absData, "exports"),
		DBPath:        filepath.Join(absData, "transleaf.db"),
		FontPath:      resolveCJKFontPath(os.Getenv("CJK_FONT_PATH")),
		MaxUploadSize: 100 << 20, // 100MB
	}

	for _, dir := range []string{cfg.DataDir, cfg.UploadsDir, cfg.CleanBgDir, cfg.ThumbnailsDir, cfg.ExportsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	// 平滑迁移旧版数据库文件，避免丢失已保存的模型配置与文档记录
	migrateLegacyDB(absData, cfg.DBPath)
	return cfg, nil
}

// migrateLegacyDB 将旧版数据库文件平滑重命名为 transleaf.db
func migrateLegacyDB(dataDir, newDBPath string) {
	if _, err := os.Stat(newDBPath); err == nil {
		return
	}
	oldDBPath := filepath.Join(dataDir, "translation_assistant.db")
	if _, err := os.Stat(oldDBPath); err != nil {
		return
	}
	if err := os.Rename(oldDBPath, newDBPath); err != nil {
		return
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		src := oldDBPath + suffix
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, newDBPath+suffix)
		}
	}
}

func resolveCJKFontPath(explicit string) string {
	candidates := []string{
		explicit,
		"./assets/fonts/NotoSansSC-Regular.ttf",
		"./backend/assets/fonts/NotoSansSC-Regular.ttf",
		"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
		"/System/Library/Fonts/Supplemental/NISC18030.ttf",
		"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
		"/usr/share/fonts/truetype/noto/NotoSansSC-Regular.ttf",
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
			return p
		}
	}
	return ""
}
