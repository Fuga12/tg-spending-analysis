// Команда classify-test — ручная проверка классификатора: разбирает фразу,
// печатает результат и потраченные токены. Нужна, чтобы до запуска бота
// убедиться, что ключ живой, схема принимается и few-shot работает (§12).
package main

import (
	"context"
	"encoding/json"
	"flag"
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
	// Разбор всегда идёт от имени конкретного человека в конкретной группе:
	// и категории, и личный словарь принадлежат ей, а не боту.
	userID := flag.Int64("user", 0, "telegram id, от чьего имени разбирать")
	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, `использование: classify-test -user <id> "вчера пятёрочка 1200 и такси 400"`)
		os.Exit(2)
	}
	text := strings.Join(flag.Args(), " ")

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
	}, log)

	if *userID == 0 {
		*userID = cfg.OwnerID()
	}
	member, ok, err := store.MemberOf(ctx, *userID)
	if err != nil {
		log.Error("поиск группы", "err", err)
		os.Exit(1)
	}
	if !ok {
		log.Error("этот человек не состоит ни в одной группе — разбирать не от чьего имени",
			"user_id", *userID)
		os.Exit(1)
	}

	// Предохранители те же, что в боевом режиме, — проверять надо то же самое.
	breaker := classify.NewBreaker(cfg.LLMBreakerCooldown, log)
	budget := classify.NewBudget(cfg.LLMMonthlyTokenBudget, store, nil, log)
	svc := classify.NewService(llm, breaker, budget, log)

	// Расход этого разбора считается как прирост llm_usage: заодно видно,
	// появилась ли там вообще строка.
	before, err := store.MonthlyUsage(ctx)
	if err != nil {
		log.Error("расход токенов", "err", err)
		os.Exit(1)
	}

	start := time.Now()
	group := store.ForGroup(member.GroupID)
	res, err := svc.Classify(ctx, classify.Scope{
		Payer: member, Dict: group, Usage: group,
	}, text)
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
	// Recipients — member_id получателей. Пустой список означает «на всю
	// группу», и это не то же самое, что «модель не поняла».
	Recipients []int64  `json:"recipients"`
	Kind       string   `json:"kind"`
	DaysAgo    int      `json:"days_ago"`
	Words      []string `json:"words"`
}

func toPrintable(items []classify.Item) []printable {
	out := make([]printable, 0, len(items))
	for _, i := range items {
		out = append(out, printable{
			Amount:      i.Amount.String(),
			Description: i.Description,
			CategoryID:  i.CategoryID,
			Recipients:  i.Recipients,
			Kind:        i.Kind,
			DaysAgo:     i.DaysAgo,
			Words:       i.Words,
		})
	}
	return out
}
