package main

import (
	"bytes"
	"flag"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// runMainBusyEnv переключает тестовый бинарь в режим «просто запусти main()»
// для сценария с занятым портом.
const runMainBusyEnv = "NOTES_API_TEST_RUN_MAIN_BUSY"

// TestMainExitsWhenPortIsBusy покрывает ветку отказа при старте: если порт
// занят, ListenAndServe возвращает ошибку, и main() обязан её залогировать и
// завершиться сам. Регрессия здесь — не «неверный текст лога», а зависший
// навсегда процесс, который слушает сигналы, но ничего не обслуживает.
//
// Тест перезапускает сам себя дочерним процессом: в дочернем режиме
// (runMainBusyEnv=1) он просто вызывает main().
func TestMainExitsWhenPortIsBusy(t *testing.T) {
	if os.Getenv(runMainBusyEnv) == "1" {
		main()
		return
	}
	if testing.Short() {
		t.Skip("поднимает реальный сервер, пропускаем в -short")
	}
	if portBusy(mainAddr) {
		t.Skipf("порт %s занят посторонним процессом, пропускаем", mainAddr)
	}

	// Занимаем тот же порт, что зашит в main(), на всех интерфейсах.
	ln, err := net.Listen("tcp", ":8080")
	if err != nil {
		t.Skipf("не удалось занять порт :8080: %v", err)
	}
	defer func() { _ = ln.Close() }()

	args := []string{"-test.run=^TestMainExitsWhenPortIsBusy$", "-test.timeout=60s"}
	// Если родительский прогон собирает покрытие, отдаём дочернему процессу тот
	// же каталог счётчиков — иначе покрытие main() потеряется.
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		args = append(args, "-test.gocoverdir="+f.Value.String())
	}

	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), runMainBusyEnv+"=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("запуск дочернего процесса: %v", err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("процесс завершился с ошибкой %v, want код 0; вывод:\n%s", err, out.String())
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("main() не завершился за 20s при занятом порте — процесс висит; вывод:\n%s", out.String())
	}

	logs := out.String()
	if !strings.Contains(logs, "server failed") {
		t.Errorf("в логах нет сообщения об ошибке старта, вывод:\n%s", logs)
	}
	if !strings.Contains(logs, "shutting down") {
		t.Errorf("не видно штатного завершения, вывод:\n%s", logs)
	}
	if strings.Contains(logs, "graceful shutdown failed") {
		t.Errorf("shutdown отработал с ошибкой, вывод:\n%s", logs)
	}
}
