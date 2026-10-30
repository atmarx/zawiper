package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atmarx/zawiper/internal/config"
)

var bays = []*config.Bay{
	{Name: "dock-a", USBPort: "10-1", SmartType: "sat", Caps: []string{"wipe"}},
	{Name: "slot1", USBPort: "4-2", USBLUN: "1", SmartType: "sat", Caps: []string{"wipe"}},
	{Name: "hba0", SASPath: "pci-0000:03:00.0-sas-phy2-lun-0", SmartType: "scsi"},
}

func TestRules(t *testing.T) {
	r := rules(bays)
	for _, want := range []string{
		`SUBSYSTEM=="block", ENV{DEVTYPE}=="disk", DEVPATH=="*/usb*/10-1/*", SYMLINK+="github.com/atmarx/zawiper/dock-a", TAG+="systemd", ENV{SYSTEMD_WANTS}+="bayd@dock-a.service"`,
		`SYMLINK+="github.com/atmarx/zawiper/dock-a-part%n"`,
		`RUN+="/usr/bin/systemctl --no-ask-password set-property --runtime bayd@dock-a.service 'DeviceAllow=/dev/zawiper/dock-a-part%n rw'"`,
		`SUBSYSTEM=="scsi_generic", DEVPATH=="*/usb*/10-1/*", SYMLINK+="github.com/atmarx/zawiper/dock-a-sg"`,
		`SUBSYSTEM=="block", ENV{DEVTYPE}=="disk", ENV{ID_PATH}=="pci-0000:03:00.0-sas-phy2-lun-0", SYMLINK+="github.com/atmarx/zawiper/hba0"`,
		`SUBSYSTEM=="scsi_generic", ENV{ID_PATH}=="pci-0000:03:00.0-sas-phy2-lun-0", SYMLINK+="github.com/atmarx/zawiper/hba0-sg"`,
		`'DeviceAllow=/dev/zawiper/hba0-sg rw'`,
		`IMPORT{builtin}="path_id"`,
		`SYMLINK+="github.com/atmarx/zawiper/hba0", TAG+="systemd", ENV{SYSTEMD_WANTS}+="bayd@hba0.service", ENV{UDISKS_IGNORE}="1", ENV{ID_FS_TYPE}=""`,
		`SYMLINK+="github.com/atmarx/zawiper/hba0-part%n", ENV{UDISKS_IGNORE}="1", ENV{ID_FS_TYPE}="", RUN+=`,
		"# bay slot1: usb_port 4-2 lun 1\n",
		`SUBSYSTEM=="block", ENV{DEVTYPE}=="disk", DEVPATH=="*/usb*/4-2/*/target*/*:1/*", SYMLINK+="github.com/atmarx/zawiper/slot1"`,
		`SUBSYSTEM=="scsi_generic", DEVPATH=="*/usb*/4-2/*/target*/*:1/*", SYMLINK+="github.com/atmarx/zawiper/slot1-sg"`,
	} {
		if !strings.Contains(r, want) {
			t.Errorf("rules lack %s\n%s", want, r)
		}
	}
	// Every rule line is one line, and the header's %%n came out as %n.
	if strings.Contains(r, "%%") || strings.Contains(r, "%!") {
		t.Errorf("format verbs leaked:\n%s", r)
	}
}

func TestDropIn(t *testing.T) {
	d := dropIn(bays[0])
	if !strings.Contains(d, "BindsTo=dev-zawiper-dock\\x2da.device\n") || !strings.Contains(d, "After=dev-zawiper-dock\\x2da.device\n") {
		t.Error(d)
	}
}

func TestEscapePath(t *testing.T) {
	for in, want := range map[string]string{
		"/dev/zawiper/dock-a": `dev-zawiper-dock\x2da`,
		"/dev/zawiper/hba_0":  "dev-zawiper-hba_0",
		"/dev/zawiper/b1":     "dev-zawiper-b1",
	} {
		if got := escapePath(in); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
		// Agree with systemd itself where it's installed.
		if out, err := exec.Command("systemd-escape", "--path", in).Output(); err == nil {
			if s := strings.TrimSpace(string(out)); s != want {
				t.Errorf("systemd-escape %s: %s", in, s)
			}
		}
	}
}

func TestWrite(t *testing.T) {
	out := t.TempDir()
	if err := write(out, bays); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"61-zawiper.rules", "units/bayd@dock-a.service.d/10-zawiper-device.conf", "units/bayd@slot1.service.d/10-zawiper-device.conf", "units/bayd@hba0.service.d/10-zawiper-device.conf"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Error(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(out, "bays")); string(b) != "dock-a\nslot1\nhba0\n" {
		t.Errorf("bays: %q", b)
	}
}
