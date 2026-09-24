package api

import "testing"

// 验证录像相对路径截取按路径分隔符对齐匹配：
// 子串误命中（oldrecordings）须回退 fallback，多次出现取最右侧存储根
// 返回值不含 storageDir 前缀
func TestRelativeRecordingPath(t *testing.T) {
	tests := []struct {
		name       string
		filePath   string
		storageDir string
		fallback   string
		want       string
	}{
		{
			name:       "标准绝对路径",
			filePath:   "/opt/app/recordings/2026-08-28/a.mp4",
			storageDir: "recordings",
			fallback:   "http://x/a.mp4",
			want:       "2026-08-28/a.mp4",
		},
		{
			name:       "子串误命中回退",
			filePath:   "/data/oldrecordings/a.mp4",
			storageDir: "recordings",
			fallback:   "http://x/a.mp4",
			want:       "http://x/a.mp4",
		},
		{
			name:       "已是相对路径去前缀",
			filePath:   "recordings/2026-08-28/a.mp4",
			storageDir: "recordings",
			fallback:   "http://x/a.mp4",
			want:       "2026-08-28/a.mp4",
		},
		{
			name:       "多次出现取最右侧",
			filePath:   "/data/recordings/backup/recordings/a.mp4",
			storageDir: "recordings",
			fallback:   "http://x/a.mp4",
			want:       "a.mp4",
		},
		{
			name:       "多级存储目录",
			filePath:   "/opt/app/configs/recordings/2026-08-28/a.mp4",
			storageDir: "configs/recordings",
			fallback:   "http://x/a.mp4",
			want:       "2026-08-28/a.mp4",
		},
		{
			name:       "完全不匹配回退",
			filePath:   "/data/videos/a.mp4",
			storageDir: "recordings",
			fallback:   "http://x/a.mp4",
			want:       "http://x/a.mp4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := relativeRecordingPath(tt.filePath, tt.storageDir, tt.fallback); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
