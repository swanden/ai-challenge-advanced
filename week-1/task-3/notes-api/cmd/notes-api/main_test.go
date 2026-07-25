package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// discardLogger — логгер в никуда: сквозным тестам вывод wiring-а не нужен.
func discardLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

const (
	// runMainEnv переключает тестовый бинарь в режим «просто запусти main()».
	runMainEnv = "NOTES_API_TEST_RUN_MAIN"
	// mainAddr — адрес, который main() зашивает в http.Server.
	mainAddr = "127.0.0.1:8080"
)

// portBusy сообщает, занят ли адрес: на занятом порту тест смысла не имеет.
func portBusy(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

// waitReady опрашивает сервер, пока он не начнёт отвечать, либо пока
// не истечёт дедлайн (или дочерний процесс не умрёт раньше времени).
func waitReady(t *testing.T, url string, deadline time.Duration) bool {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	until := time.Now().Add(deadline)
	for time.Now().Before(until) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestMainServesAndShutsDownGracefully проверяет точку входа целиком:
// main() поднимает сервис на своём адресе, отдаёт API и UI, а по SIGTERM
// завершается штатно (код выхода 0) и освобождает порт.
//
// Тест перезапускает сам себя дочерним процессом: в дочернем режиме
// (runMainEnv=1) он просто вызывает main().
func TestMainServesAndShutsDownGracefully(t *testing.T) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		return
	}
	if testing.Short() {
		t.Skip("поднимает реальный сервер, пропускаем в -short")
	}
	if portBusy(mainAddr) {
		t.Skipf("порт %s занят, пропускаем сквозной запуск main()", mainAddr)
	}

	args := []string{"-test.run=^TestMainServesAndShutsDownGracefully$", "-test.timeout=60s"}
	// Если родительский прогон собирает покрытие, отдаём дочернему процессу тот
	// же каталог счётчиков — иначе покрытие main() потеряется.
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		args = append(args, "-test.gocoverdir="+f.Value.String())
	}

	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
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

	base := "http://" + mainAddr
	if !waitReady(t, base+"/notes", 15*time.Second) {
		t.Fatalf("сервер не поднялся на %s, вывод процесса:\n%s", mainAddr, out.String())
	}

	// Боевая сборка зависимостей действительно обслуживает API...
	resp, err := http.Post(base+"/notes", "application/json", strings.NewReader(`{"text":"  from main  "}`))
	if err != nil {
		t.Fatalf("POST /notes: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /notes status = %d, want %d (body %q)", resp.StatusCode, http.StatusCreated, body)
	}
	var created struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("unmarshal note %q: %v", body, err)
	}
	if created.Text != "from main" {
		t.Fatalf("text = %q, want %q — в боевой сборке потерялась нормализация", created.Text, "from main")
	}
	if created.ID == "" {
		t.Fatal("id пустой — в боевой сборке не подключён генератор идентификаторов")
	}

	// ...и отдаёт встроенный UI по корню.
	uiResp, err := http.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	ui, _ := io.ReadAll(uiResp.Body)
	_ = uiResp.Body.Close()
	if uiResp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", uiResp.StatusCode, http.StatusOK)
	}
	if !strings.Contains(strings.ToLower(string(ui)), "<html") {
		t.Fatalf("GET / вернул не страницу: %q", ui)
	}

	// SIGTERM обязан привести к штатному завершению, а не к убийству процесса.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("отправка SIGTERM: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("процесс завершился с ошибкой %v, want код 0; вывод:\n%s", err, out.String())
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("процесс не завершился по SIGTERM за 20s, вывод:\n%s", out.String())
	}

	logs := out.String()
	for _, want := range []string{"server starting", ":8080", "shutting down"} {
		if !strings.Contains(logs, want) {
			t.Errorf("в логах нет %q, вывод:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "graceful shutdown failed") {
		t.Errorf("graceful shutdown отработал с ошибкой, вывод:\n%s", logs)
	}
	if portBusy(mainAddr) {
		t.Errorf("порт %s не освобождён после завершения", mainAddr)
	}
}
