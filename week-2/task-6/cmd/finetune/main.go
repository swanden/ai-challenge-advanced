// Команда finetune автоматизирует запуск дообучения через OpenAI API:
// загрузка файла, создание задания, опрос статуса до терминального.
//
// Каждый шаг пишется в журнал прогона вместе с кодом ответа и телом —
// в том числе неуспешный. Это сделано намеренно: с мая 2026 OpenAI
// сворачивает self-serve файнтюн, и организации, которые раньше не
// запускали обучение, получают отказ на шаге создания задания.
// Отказ — тоже результат, и он должен быть зафиксирован, а не потерян
// в выводе терминала.
//
// Порядок запуска:
//
//	go run ./week-2/task-6/cmd/finetune -stop-after upload   # проверить, что файл принимается
//	go run ./week-2/task-6/cmd/finetune                      # полный цикл
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-2/task-6/internal/dataset"
	"github.com/swanden/ai-challenge-advanced/week-2/task-6/internal/envfile"
)

type step struct {
	Name     string    `json:"name"`
	Method   string    `json:"method"`
	URL      string    `json:"url"`
	Status   int       `json:"status"`
	Body     string    `json:"body"`
	At       time.Time `json:"at"`
	Duration string    `json:"duration"`
}

type journal struct {
	StartedAt time.Time `json:"started_at"`
	BaseURL   string    `json:"base_url"`
	Model     string    `json:"model"`
	Steps     []step    `json:"steps"`
	Outcome   string    `json:"outcome"`
}

func main() {
	trainPath := flag.String("file", "week-2/task-6/dataset/train.jsonl", "файл обучения")
	valPath := flag.String("validation-file", "week-2/task-6/dataset/eval.jsonl", "файл валидации; пустая строка — не отправлять")
	model := flag.String("model", "gpt-4o-mini-2024-07-18", "базовая модель")
	suffix := flag.String("suffix", "task-router", "суффикс имени дообученной модели")
	baseURL := flag.String("base-url", "https://api.openai.com/v1", "базовый URL API")
	keyEnv := flag.String("key-env", "OPENAI_API_KEY", "переменная окружения с ключом")
	pollEvery := flag.Duration("poll", 20*time.Second, "интервал опроса статуса")
	timeout := flag.Duration("timeout", 60*time.Minute, "предел ожидания терминального статуса")
	stopAfter := flag.String("stop-after", "poll", "докуда идти: upload, create или poll")
	outDir := flag.String("out", "week-2/task-6/evidence", "куда положить журнал прогона")
	envPath := flag.String("env-file", "", "файл окружения; по умолчанию ищется .env в текущем каталоге и выше")
	flag.Parse()

	if path, err := envfile.Load(*envPath); err != nil {
		fail("%v", err)
	} else if path != "" {
		fmt.Printf("переменные окружения прочитаны из %s\n", path)
	}

	// Файл проверяется до отправки: платить за загрузку заведомо
	// битого датасета незачем.
	if _, err := dataset.LoadExamples(*trainPath); err != nil {
		fail("файл обучения не прошёл проверку: %v", err)
	}

	key := os.Getenv(*keyEnv)
	if key == "" {
		fail("переменная %s пуста", *keyEnv)
	}

	c := &http.Client{Timeout: 120 * time.Second}
	j := &journal{StartedAt: time.Now(), BaseURL: *baseURL, Model: *model}
	defer func() { save(j, *outDir) }()

	// Шаг 1. Загрузка файла обучения.
	trainID, err := uploadFile(c, j, *baseURL, key, *trainPath)
	if err != nil {
		j.Outcome = "остановились на загрузке файла: " + err.Error()
		fmt.Fprintln(os.Stderr, "finetune: "+j.Outcome)
		os.Exit(1)
	}
	fmt.Printf("файл обучения загружен: %s\n", trainID)

	var valID string
	if *valPath != "" {
		valID, err = uploadFile(c, j, *baseURL, key, *valPath)
		if err != nil {
			// Валидационный файл не критичен: продолжаем без него.
			fmt.Fprintf(os.Stderr, "finetune: файл валидации не загружен (%v), продолжаю без него\n", err)
			valID = ""
		} else {
			fmt.Printf("файл валидации загружен: %s\n", valID)
		}
	}
	if *stopAfter == "upload" {
		j.Outcome = "остановлено после загрузки по флагу -stop-after"
		fmt.Println(j.Outcome)
		return
	}

	// Шаг 2. Создание задания.
	jobID, err := createJob(c, j, *baseURL, key, *model, *suffix, trainID, valID)
	if err != nil {
		j.Outcome = "задание не создано: " + err.Error()
		fmt.Fprintln(os.Stderr, "finetune: "+j.Outcome)
		fmt.Fprintln(os.Stderr, "если API отвечает отказом на самом создании задания — это ожидаемо: "+
			"OpenAI сворачивает self-serve файнтюн, ответ сервера сохранён в журнале прогона")
		os.Exit(1)
	}
	fmt.Printf("задание создано: %s\n", jobID)
	if *stopAfter == "create" {
		j.Outcome = "остановлено после создания задания по флагу -stop-after"
		fmt.Println(j.Outcome)
		return
	}

	// Шаг 3. Опрос статуса до терминального.
	status, err := poll(c, j, *baseURL, key, jobID, *pollEvery, *timeout)
	if err != nil {
		j.Outcome = "опрос прерван: " + err.Error()
		fmt.Fprintln(os.Stderr, "finetune: "+j.Outcome)
		os.Exit(1)
	}
	j.Outcome = "терминальный статус: " + status
	fmt.Println(j.Outcome)
}

func uploadFile(c *http.Client, j *journal, baseURL, key, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("purpose", "fine-tune"); err != nil {
		return "", err
	}
	part, err := w.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	url := strings.TrimRight(baseURL, "/") + "/files"
	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", w.FormDataContentType())

	raw, status, err := do(c, j, "upload:"+filepath.Base(path), req)
	if err != nil {
		return "", err
	}
	if status/100 != 2 {
		return "", fmt.Errorf("HTTP %d: %s", status, trim(raw))
	}
	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if parsed.ID == "" {
		return "", fmt.Errorf("в ответе нет идентификатора файла")
	}
	return parsed.ID, nil
}

func createJob(c *http.Client, j *journal, baseURL, key, model, suffix, trainID, valID string) (string, error) {
	payload := map[string]any{
		"training_file": trainID,
		"model":         model,
	}
	if suffix != "" {
		payload["suffix"] = suffix
	}
	if valID != "" {
		payload["validation_file"] = valID
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	url := strings.TrimRight(baseURL, "/") + "/fine_tuning/jobs"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	raw, status, err := do(c, j, "create-job", req)
	if err != nil {
		return "", err
	}
	if status/100 != 2 {
		return "", fmt.Errorf("HTTP %d: %s", status, trim(raw))
	}
	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if parsed.ID == "" {
		return "", fmt.Errorf("в ответе нет идентификатора задания")
	}
	return parsed.ID, nil
}

func poll(c *http.Client, j *journal, baseURL, key, jobID string, every, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	url := strings.TrimRight(baseURL, "/") + "/fine_tuning/jobs/" + jobID
	for {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+key)

		raw, status, err := do(c, j, "poll", req)
		if err != nil {
			return "", err
		}
		if status/100 != 2 {
			return "", fmt.Errorf("HTTP %d: %s", status, trim(raw))
		}
		var parsed struct {
			Status         string `json:"status"`
			FineTunedModel string `json:"fine_tuned_model"`
			Error          struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return "", err
		}
		fmt.Printf("статус: %s\n", parsed.Status)

		switch parsed.Status {
		case "succeeded":
			fmt.Printf("готовая модель: %s\n", parsed.FineTunedModel)
			return parsed.Status, nil
		case "failed", "cancelled":
			if parsed.Error.Message != "" {
				fmt.Fprintf(os.Stderr, "причина: %s\n", parsed.Error.Message)
			}
			return parsed.Status, nil
		}
		if time.Now().After(deadline) {
			return parsed.Status, fmt.Errorf("предел ожидания %s исчерпан", timeout)
		}
		time.Sleep(every)
	}
}

// do выполняет запрос и записывает шаг в журнал независимо от исхода.
func do(c *http.Client, j *journal, name string, req *http.Request) ([]byte, int, error) {
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		j.Steps = append(j.Steps, step{
			Name: name, Method: req.Method, URL: req.URL.String(),
			Status: 0, Body: "транспортная ошибка: " + err.Error(),
			At: start, Duration: time.Since(start).String(),
		})
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	j.Steps = append(j.Steps, step{
		Name: name, Method: req.Method, URL: req.URL.String(),
		Status: resp.StatusCode, Body: trim(raw),
		At: start, Duration: time.Since(start).String(),
	})
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

func save(j *journal, outDir string) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "finetune: журнал не сохранён: %v\n", err)
		return
	}
	path := filepath.Join(outDir, "finetune-"+j.StartedAt.Format("20060102-150405")+".json")
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "finetune: журнал не сохранён: %v\n", err)
		return
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "finetune: журнал не сохранён: %v\n", err)
		return
	}
	fmt.Printf("журнал прогона: %s\n", path)
}

func trim(b []byte) string {
	s := strings.TrimSpace(string(b))
	const max = 2000
	if len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "finetune: "+format+"\n", args...)
	os.Exit(1)
}
