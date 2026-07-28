// Команда classify-test — ручная проверка классификатора: разбирает фразу,
// печатает результат и потраченные токены. Нужна, чтобы до запуска бота
// убедиться, что ключ живой, схема принимается и few-shot работает (§12).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"budget/internal/classify"
	"budget/internal/config"
	"budget/internal/storage"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, `использование: classify-test "вчера пятёрочка 1200 и такси 400"`)
		os.Exit(2)
	}
	text := strings.Join(os.Args[1:], " ")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg, err := config.Load()
	if err != nil {
		log.Error("конфиг", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store, err := storage.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("БД", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	llm := classify.NewYandex(classify.YandexConfig{
		BaseURL:  cfg.LLMBaseURL,
		APIKey:   cfg.YandexAPIKey,
		FolderID: cfg.YandexFolderID,
		Model:    cfg.LLMModel,
		Timeout:  cfg.LLMTimeout,
	}, store, log)

	// Разбор идёт от имени владельца бота: у него свой личный кэш слов.
	svc := classify.NewService(store, llm, log)

	// Расход этого разбора считается как прирост llm_usage: заодно видно,
	// появилась ли там вообще строка.
	before, err := store.MonthlyUsage(ctx)
	if err != nil {
		log.Error("расход токенов", "err", err)
		os.Exit(1)
	}

	start := time.Now()
	res, err := svc.Classify(ctx, cfg.AllowedUserIDs[0], text)
	elapsed := time.Since(start)
	after, usageErr := store.MonthlyUsage(ctx)

	if err != nil {
		log.Error("разбор не удался", "err", err, "kind", classify.ErrKind(err))
		if usageErr == nil {
			printUsage(before, after)
		}
		os.Exit(1)
	}

	fmt.Printf("источник: %s, за %s\n", res.Source, elapsed.Round(time.Millisecond))
	out, _ := json.MarshalIndent(toPrintable(res.Items), "", "  ")
	fmt.Println(string(out))

	if usageErr != nil {
		log.Error("расход токенов", "err", usageErr)
		return
	}
	printUsage(before, after)
}

func printUsage(before, after storage.MonthUsage) {
	fmt.Printf("этот разбор: %d токенов (%d промпт + %d ответ), вызовов API %d\n",
		after.TotalTokens-before.TotalTokens,
		after.PromptTokens-before.PromptTokens,
		after.CompletionTokens-before.CompletionTokens,
		after.Calls-before.Calls)
	fmt.Printf("за месяц:    %d токенов, вызовов %d, из них неуспешных %d\n",
		after.TotalTokens, after.Calls, after.Failed)
}

type printable struct {
	Amount      string `json:"amount"`
	Description string `json:"description"`
	CategoryID  *int32 `json:"category_id"`
	Beneficiary string `json:"beneficiary"`
	Kind        string `json:"kind"`
	DaysAgo     int    `json:"days_ago"`
	Words       []string
}

func toPrintable(items []classify.Item) []printable {
	out := make([]printable, 0, len(items))
	for _, i := range items {
		out = append(out, printable{
			Amount:      i.Amount.String(),
			Description: i.Description,
			CategoryID:  i.CategoryID,
			Beneficiary: i.Beneficiary,
			Kind:        i.Kind,
			DaysAgo:     i.DaysAgo,
			Words:       i.Words,
		})
	}
	return out
}
