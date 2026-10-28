package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parseString(t *testing.T, name, body string) (*Bay, error) {
	t.Helper()
	return ParseBay("/etc/zawiper/bays/"+name+".toml", strings.NewReader(body))
}

func TestUSBBay(t *testing.T) {
	b, err := parseString(t, "dock-a", `
# the caddy on lab-b, passed through by port
name = "dock-a"
usb_port = "10-1"   # as the VM's sysfs names it
caps = "wipe"
`)
	if err != nil {
		t.Fatal(err)
	}
	if b.USBPort != "10-1" || b.SASPath != "" || b.SmartType != "sat" || !b.Can("wipe") || b.Can("load") {
		t.Fatalf("%+v", b)
	}
	if b.Selector() != "usb_port 10-1" {
		t.Error(b.Selector())
	}
}

func TestSASBay(t *testing.T) {
	b, err := parseString(t, "hba0", `name = "hba0"
sas_path = "pci-0000:03:00.0-sas-phy2-lun-0"
caps = ["wipe", "sas"]`)
	if err != nil {
		t.Fatal(err)
	}
	if b.SmartType != "scsi" || !b.Can("sas") || !b.Can("wipe") {
		t.Fatalf("%+v", b)
	}
}

func TestReadOnlyBay(t *testing.T) {
	b, err := parseString(t, "b1", "name = \"b1\"\nusb_port = \"3-2.4\"\nsmart_type = \"sat\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Caps) != 0 || b.Can("wipe") {
		t.Fatalf("no caps should mean no wipe: %+v", b)
	}
}

func TestRefusals(t *testing.T) {
	for _, c := range []struct{ file, body, want string }{
		{"bay", `name = "the bay"` + "\nport = \"10-1\"", "Phase 1 key"},
		{"b1", `name = "b1"`, "no selector"},
		{"b1", "name = \"b1\"\nusb_port = \"10-1\"\nsas_path = \"pci-0000:03:00.0-sas-phy2-lun-0\"", "exactly one selector"},
		{"b1", "name = \"b2\"\nusb_port = \"10-1\"", "should be b2.toml"},
		{"The Bay", "name = \"The Bay\"\nusb_port = \"10-1\"", "bay name"},
		{"b1", "name = \"b1\"\nusb_port = \"sdc\"", "isn't a sysfs port"},
		{"b1", "name = \"b1\"\nusb_port = \"10-1/../2\"", "isn't a sysfs port"},
		{"b1", "name = \"b1\"\nusb_lun = \"1\"\nsas_path = \"pci-0000:03:00.0-sas-phy2-lun-0\"", "without usb_port"},
		{"b1", "name = \"b1\"\nusb_port = \"10-1\"\nusb_lun = \"*\"", "isn't a LUN number"},
		{"b1", "name = \"b1\"\nsas_path = \"/dev/sda\"", "isn't a SAS by-path"},
		{"b1", "name = \"b1\"\nsas_path = \"pci-0000:00:17.0-ata-1\"", "isn't a SAS by-path"},
		{"b1", "name = \"b1\"\nsas_path = \"pci-0000:03:00.0-sas-phy2-lun-0-part1\"", "names a partition"},
		{"b1", "name = \"b1\"\nusb_port = \"10-1\"\nsmart_type = \"nvme\"", "smart_type"},
		{"b1", "name = \"b1\"\nusb_port = \"10-1\"\ncaps = \"wipe, format\"", "unknown cap"},
		{"b1", "name = \"b1\"\nusb_port = \"10-1\"\ncaps = [\"wipe\", \"wipe\"]", "twice"},
		{"b1", "name = \"b1\"\nusb-port = \"10-1\"", "unknown key"},
		{"b1", "name = \"b1\"\nusb_port = 10", "double-quoted"},
		{"b1", "name = \"b1\"\nusb_port = \"10-1\" extra", "unexpected"},
		{"b1", "name = \"b1\"\nname = \"b1\"\nusb_port = \"10-1\"", "set twice"},
		{"b1", "name = \"b1\"\nusb_port = [\"10-1\"]", "not a list"},
		{"b1", "name = \"b1\"\nusb_port = \"10-1\"\ncaps = [\"wipe\" \"load\"]", "expected , or ]"},
	} {
		_, err := parseString(t, c.file, c.body)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.body, err, c.want)
		}
	}
}

func TestLoadBays(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("b.toml", "name = \"b\"\nsas_path = \"pci-0000:03:00.0-sas-phy2-lun-0\"\n")
	write("a.toml", "name = \"a\"\nusb_port = \"10-1\"\n")
	write("notes.txt", "not a bay")
	bays, err := LoadBays(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(bays) != 2 || bays[0].Name != "a" || bays[1].Name != "b" {
		t.Fatalf("%+v", bays)
	}

	// Copy a bay's file to make a new one, rename it, forget the selector.
	write("c.toml", "name = \"c\"\nusb_port = \"10-1\"\n")
	if _, err := LoadBays(dir); err == nil || !strings.Contains(err.Error(), "One device, one bay") {
		t.Fatalf("two bays on one port: %v", err)
	}

	// A two-slot dock: one bay per LUN is fine, and a whole-port bay or a
	// repeated LUN is not.
	write("a.toml", "name = \"a\"\nusb_port = \"10-1\"\nusb_lun = \"0\"\n")
	write("c.toml", "name = \"c\"\nusb_port = \"10-1\"\nusb_lun = \"1\"\n")
	bays, err = LoadBays(dir)
	if err != nil || len(bays) != 3 || bays[2].Selector() != "usb_port 10-1 lun 1" {
		t.Fatalf("one bay per slot: %v %+v", err, bays)
	}
	a, _ := LoadBay(filepath.Join(dir, "a.toml"))
	b, _ := LoadBay(filepath.Join(dir, "b.toml"))
	if got := Siblings(dir, a); len(got) != 1 || got[0] != "c" {
		t.Errorf("a's siblings: %v", got)
	}
	if got := Siblings(dir, b); got != nil {
		t.Errorf("a SAS bay has no siblings: %v", got)
	}
	for _, c := range []string{"name = \"d\"\nusb_port = \"10-1\"\n", "name = \"d\"\nusb_port = \"10-1\"\nusb_lun = \"1\"\n"} {
		write("d.toml", c)
		if _, err := LoadBays(dir); err == nil || !strings.Contains(err.Error(), "One device, one bay") {
			t.Errorf("%q beside slots 0 and 1: %v", c, err)
		}
	}
}
