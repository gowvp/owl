package conf

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ixugo/goddd/pkg/orm"
)

func DefaultConfig() Bootstrap {
	return Bootstrap{
		Server: Server{
			Username:   "admin",
			Password:   "admin",
			RTMPSecret: "123",
			HTTP: ServerHTTP{
				Port:      15123,
				Timeout:   Duration(60 * time.Second),
				JwtSecret: orm.GenerateRandomString(24),
				APISecret: strings.ReplaceAll(uuid.New().String(), "-", ""),
				AuthURL:   "",
				PProf: ServerPPROF{
					Enabled:   true,
					AccessIps: []string{"::1", "127.0.0.1"},
				},
			},
			Webhook: ServerWebhook{
				RecvSecret: uuid.New().String(),
			},
			AI: ServerAI{
				Disabled:             false,
				RetainDays:           7,
				AnalysisInterval:     2.0,
				AlertCooldownSeconds: 10,
			},
			Recording: ServerRecording{
				Disabled:           false,
				StorageDir:         "./configs/recordings",
				RetainDays:         3,
				DiskUsageThreshold: 95.0,
				SegmentSeconds:     300,
			},
		},
		Data: Data{
			Database: Database{
				Dsn:             "./configs/data.db",
				MaxIdleConns:    10,
				MaxOpenConns:    50,
				ConnMaxLifetime: Duration(6 * time.Hour),
				SlowThreshold:   Duration(200 * time.Millisecond),
			},
		},
		Sip: SIP{
			Port:     15060,
			ID:       "34010000002000000001",
			Password: "",
		},
		Media: Media{
			IP:           "127.0.0.1",
			HTTPPort:     80,
			Secret:       "",
			WebHookIP:    "127.0.0.1",
			SDPIP:        "127.0.0.1",
			RTPPortRange: "20000-20100",
			Type:         "zlm",
		},
		Log: Log{
			Dir:          "./logs",
			Level:        "error",
			MaxAge:       0,
			MaxDays:      7,
			MaxSize:      50,
			RotationTime: Duration(12 * time.Hour),
		},
	}
}
