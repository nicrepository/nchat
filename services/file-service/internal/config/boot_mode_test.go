package config

import "testing"

func TestBootModes(t *testing.T) {
	for _, mode := range []string{"", "combined", "api", "scan-worker", "preview-worker"} {
		t.Run(mode, func(t *testing.T) {
			cfg := Config{BootMode: mode, UploadsEnabled: true, MalwareScanRequired: true, MalwareScannerAddress: "clamav:3310"}
			if err := cfg.validateBootMode(); err != nil {
				t.Fatal(err)
			}
			if cfg.IsWorker() == cfg.ServesAPI() {
				t.Fatal("each mode must have exactly one operational role")
			}
		})
	}
	for _, mode := range []string{"worker", "API", " api", "all", "scan-worker,api"} {
		if err := (Config{BootMode: mode}).Validate(); err == nil {
			t.Fatalf("accepted invalid mode %q", mode)
		}
	}
}

func TestWorkerModeRejectsIncompleteOrAmbiguousConfiguration(t *testing.T) {
	valid := Config{BootMode: "scan-worker", UploadsEnabled: true, MalwareScanRequired: true, MalwareScannerAddress: "clamav:3310"}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.UploadsEnabled = false },
		func(c *Config) { c.MalwareScanRequired = false },
		func(c *Config) { c.MalwareScannerAddress = "" },
		func(c *Config) { c.LinkPreviewEnabled = true },
		func(c *Config) { c.LinkSafetyEnabled = true },
	} {
		cfg := valid
		mutate(&cfg)
		if err := cfg.validateBootMode(); err == nil {
			t.Fatal("accepted incomplete worker configuration")
		}
	}
}

func TestLoadBootMode(t *testing.T) {
	t.Setenv("FILE_BOOT_MODE", "preview-worker")
	if cfg := Load(); cfg.BootMode != "preview-worker" {
		t.Fatalf("mode = %q", cfg.BootMode)
	}
}

func TestPreviewConverterValidationRespectsLoadedDefaults(t *testing.T) {
	enableUploads(t)
	t.Setenv("FILE_BOOT_MODE", "preview-worker")
	t.Setenv("DOCUMENT_CONVERTER_URL", "http://127.0.0.1:8089")
	t.Setenv("DOCUMENT_CONVERTER_TIMEOUT_SECONDS", "0")
	cfg := Load()
	if cfg.DocumentConverterTimeoutSeconds != 35 {
		t.Fatalf("converter timeout default = %d", cfg.DocumentConverterTimeoutSeconds)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.DocumentConverterTimeoutSeconds = 40
	if err := cfg.Validate(); err == nil {
		t.Fatal("preview accepted invalid converter timeout")
	}
	cfg.BootMode = "combined"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("combined configuration compatibility changed: %v", err)
	}
}
