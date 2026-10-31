package radiolock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestASecondClaimIsRefusedUntilReleased(t *testing.T) {
	dir := t.TempDir()
	release, err := Claim(dir, "BLE", "CC:68:00:00:00:01")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Claim(dir, "BLE", "cc:68:00:00:00:01"); !errors.Is(err, ErrInUse) {
		t.Errorf("second claim: %v", err)
	}
	other, err := Claim(dir, "BLE", "CC:68:00:00:00:02")
	if err != nil {
		t.Errorf("another radio: %v", err)
	}
	other()
	release()
	again, err := Claim(dir, "BLE", "CC:68:00:00:00:01")
	if err != nil {
		t.Errorf("after release: %v", err)
	}
	again()
}

func TestAPortReachedByAnotherNameIsTheSameRadio(t *testing.T) {
	dir := t.TempDir()
	port := filepath.Join(dir, "ttyACM0")
	os.WriteFile(port, nil, 0o600)
	link := filepath.Join(dir, "by-id")
	if os.Symlink(port, link) != nil {
		t.Skip("no symlinks here")
	}
	release, _ := Claim(dir, "USB", port)
	defer release()
	if _, err := Claim(dir, "USB", link); !errors.Is(err, ErrInUse) {
		t.Errorf("by its other name: %v", err)
	}
}

// Another process is what the lock is for, so one is started holding it.
func TestAnotherProcessIsRefused(t *testing.T) {
	if dir := os.Getenv("RADIOLOCK_HOLD"); dir != "" {
		Claim(dir, "TCP", "radio.local:5000")
		os.Stdout.WriteString("held\n")
		os.Stdin.Read(make([]byte, 1))
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestAnotherProcessIsRefused")
	cmd.Env = append(os.Environ(), "RADIOLOCK_HOLD="+dir)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer in.Close()
	buf := make([]byte, 5)
	if _, err := out.Read(buf); err != nil {
		t.Fatal(err)
	}
	if _, err := Claim(dir, "TCP", "radio.local:5000"); !errors.Is(err, ErrInUse) {
		t.Errorf("while another process holds it: %v", err)
	}
	in.Close()
	cmd.Wait()
	release, err := Claim(dir, "TCP", "radio.local:5000")
	if err != nil {
		t.Errorf("after that process ended: %v", err)
	}
	release()
}
