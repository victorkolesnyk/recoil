# Зміни для форку victorkolesnyk/recoil

## Нові файли
- **mcp.go** — MCP-сервер (stdio, JSON-RPC 2.0), 5 інструментів:
  recoil_project, recoil_recall, recoil_encode, recoil_guard, recoil_analyse.
  Severity-система (fatal/blocker/lesson/pattern), автодетекція повторних
  помилок, ізоляція пам'яті по проєктах, security-хардening
  (захист від path traversal, обмеження розміру вводу, права доступу 0600/0700).
- **mcp_test.go** — 16 unit-тестів на безпеку та нову логіку
  (path traversal, sanitize, severity, pattern detection, ізоляція проєктів,
  права доступу до файлів).

## Змінені файли
- **main.go** — додано команду `recoil serve --mcp` (+15 рядків, нічого
  не видалено, нічого не зламано в існуючому CLI).
- **go.mod** — без змін відносно оригіналу (go 1.26). Залишено як є.

## Як перевірити перед заливкою на GitHub

```bash
cd recoil
go build ./...        # має зібратись без помилок
go vet ./...           # має пройти без попереджень
go test ./... -v       # 27 тестів, всі мають бути PASS
```

## Як залити у твій форк

1. Скопіюй ці 4 файли поверх відповідних у `victorkolesnyk/recoil`
2. `git add mcp.go mcp_test.go main.go go.mod`
3. `git commit -m "Add MCP server: project isolation, severity classification, pattern detection"`
4. `git push`
