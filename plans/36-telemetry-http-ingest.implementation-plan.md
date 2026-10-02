# 36. HTTP-приём фронтенд-телеметрии (performance + errors + logs)

Парный план: [megaapp-front/plans/36-frontend-telemetry-unification.implementation-plan.md](../../megaapp-front/plans/36-frontend-telemetry-unification.implementation-plan.md). Главный по объёму — фронт, этот план — второстепенный.

**Дальнейшее развитие:** маршрут, формат запроса, раскладка файлов и флаг включения из этого плана заменяются единым приёмом — [42-extension-log-ingest](42-extension-log-ingest.implementation-plan.md) (фронт: [42-ingest-contract-migration](../../megaapp-front/plans/42-ingest-contract-migration.implementation-plan.md)). Ротация и запись остались, но вынесены в общий пакет.

## Цель

Заменить временный WS-хендлер `PERFORMANCE_METRICS_BATCH` на обычный authenticated HTTP-endpoint, принимающий любой `TelemetryEvent` (performance/error/log), сохранив рабочий механизм ротации/архивации NDJSON. Все события — в одном общем файле, без деления по типу: различение — по namespace поля `operation` (`app.*`, `error.*`, `log.*`) внутри записи, поиск по единому большому NDJSON остаётся быстрым (`grep`/`jq`), разносить по файлам незачем.

## Текущая реализация

- `internal/ws/performance_metrics.go`: `PerformanceMetricsWriter` — append-only NDJSON, ротация перед 1 GB (timestamped active-файл), асинхронная zip-архивация в `logs-archive`, миграция legacy-файла. `NewPerformanceMetricsHandler` — WS `MessageHandler`, зарегистрирован в `buildWSModule` ([modules.go:94](internal/httpx/modules.go:94)). Флаг `PerformanceMetricsEnabled`/`PERFORMANCE_METRICS_ENABLED`, по умолчанию `false`.
- userId/clientId сейчас берутся из аутентифицированного WS-клиента (`client.UserID()`, `client.clientID`), не из payload.
- Обычные HTTP-модули (`money`, `food`, `settings`) — устоявшийся паттерн: `RegisterRoutes(router, authService, handler)`, `auth.Middleware(authService)` на группе роутера, userID — `auth.IdentityFromContext(r.Context())` (см. [money/http.go](internal/money/http.go)). Cross-origin POST дополнительно защищён `auth.OriginMiddleware` внутри `Middleware`.
- Обычные app-логи (`slog.NewJSONHandler` в stdout, [platform/log/logger.go](internal/platform/log/logger.go)) — отдельный, не файловый механизм, не затрагивается этим планом.

## Решение

### Новый пакет

`internal/telemetry` вместо `internal/ws/performance_metrics.go`: `http.go` (`RegisterRoutes` + `Handler`, по образцу `money`/`food`), `writer.go` (`EventWriter` — переносит ротацию/zip/legacy-migration из `PerformanceMetricsWriter` один в один, только с новым именем файла).

### Endpoint

- `POST /api/telemetry/events`, `auth.Middleware(authService)` в группе роутера — как у `money`/`food`.
- Payload — батч `TelemetryEvent` (та же форма полей, что раньше уходила в WS batch; поля `message`/`stack` опциональны для error-событий, дискриминирующего поля нет).
- userId — из `auth.IdentityFromContext(r.Context())`, не из payload. clientId — из заголовка `X-Client-ID` (уже отправляется `AuthInterceptor` фронта на каждый обычный HTTP-запрос — отдельного WS client id не нужно).
- Ответ 200 только после успешных `append`+`fsync`+`close` — так же строго, как раньше был ack. Любая другая ситуация (ошибка записи, невалидный батч) — событие остаётся в очереди фронта до следующей попытки. Отдельного ack-payload с перечнем `eventId` не нужно: сам HTTP-статус подтверждает весь принятый батч целиком.

### Файл

- `DATA_DIR/frontend-telemetry-<ts>.ndjson` — единый файл для всех событий (было `frontend-performance-<ts>.ndjson`, только под performance). Ротация 1 GB и zip-архивация в `logs-archive` — без изменений в механизме, просто новый префикс имени.
- Никакого разбиения по типу события: `operation` внутри записи и так позволяет отфильтровать нужное (`error.*`, `app.*`, `log.*`) обычным `grep`/`jq` по одному файлу.

### Флаг

- `PERFORMANCE_METRICS_ENABLED` → единый `TELEMETRY_ENABLED` (переименование), default `true` — канал общий для performance+errors+logs, отдельного выключения по типу события нет.
- При `false` — 200 без записи на диск (аналог текущего `discarded`), чтобы фронт не копил бесконечную локальную очередь.
- Обновить `.env.prod.example`/`.env.test.example`.

### Валидация входа

Переносим текущие проверки `decodePerformanceMetricsBatch` (структура декодируется, батч ограничен размером/числом событий, `eventId` и `operation` обязательны, значения не могут быть неограниченными) в `decodeTelemetryBatch` — без проверки типа события, его просто нет. Невалидный батч — `400`, без записи файла и без падения хендлера — как и раньше.

### Что убирается

- WS-хендлер `PERFORMANCE_METRICS_BATCH`, его регистрация в `buildWSModule`/`modules.go`.
- `PERFORMANCE_METRICS_ACK` (WS message type — убирается симметрично на фронте, план 36).
- WS-специфичный `decodePerformanceMetricsBatch` с извлечением `userID`/`clientID` из `ws.Client` — заменяется на извлечение из HTTP request context/заголовка.

## Чеклист

- ✅ Создать `internal/telemetry`: перенести `EventWriter` (ротация/zip/legacy-migration) из `internal/ws/performance_metrics.go`, сменить префикс файла на `frontend-telemetry`. (Legacy single-file migration отброшена — она была для стороннего давно устаревшего имени файла с исходного временного запуска в июле 2025, к новой схеме отношения не имеет.)
- ✅ Добавить `POST /api/telemetry/events`: `RegisterRoutes`+`Handler` по паттерну `money`/`food`, `auth.Middleware`, userID из identity в контексте, clientId из заголовка `X-Client-ID`.
- ✅ Перенести валидацию батча (`encodeBatch`) без проверки типа события — `operation`/`eventId` обязательны, размер/число ограничены как раньше.
- ✅ Переименовать флаг в единый `TELEMETRY_ENABLED` (default `true`), обновить `config.go` и `.env.*.example`.
- ✅ Удалить WS-хендлер `PERFORMANCE_METRICS_BATCH`, его регистрацию в `httpx/modules.go`, `PERFORMANCE_METRICS_ACK`.
- ✅ Перенести/переименовать unit-тесты (`performance_metrics_test.go` → пакет `telemetry`, `writer_test.go`/`batch_test.go`/`http_test.go`).
- ✅ Прогнать `go build`/`go vet`/`go test ./...` — зелено.
- ✅ Проверить, что `sendBeacon`-запрос с фронта проходит `auth.OriginMiddleware` (same-origin проверка). Разобрано статически: `isSameOrigin` пропускает запрос и при отсутствующем `Origin`, и при `Origin`, совпадающем с `r.Host` — фронт и бэк на одном origin в проде (бэк раздаёт `PublicDir`), а в dev проксируются через webpack-dev-server на уровне HTTP, что прозрачно и для `sendBeacon`. Живая проверка в браузере не выполнялась (нет доступа к браузерным инструментам в этой сессии) — стоит перепроверить на реальном деплое.

## Архитектурный самоаудит (12.09.2026, по правилу "No Lingering Workarounds")

Проверка: не является ли что-то в получившемся коде обходным решением, которое стоило сразу довести до архитектурно верного.

**Найдено — реальное дублирование, не гипотетическое.** `internal/telemetry/writer.go` (`Append`) и `internal/metrics/outbox.go` (`appendOutboxLine`) независимо реализуют один и тот же низкоуровневый примитив: открыть файл на дозапись, записать байты, `fsync`, закрыть, отследить размер, ротировать при превышении порога. `internal/ws/performance_metrics.go` до этой сессии уже дублировал его один раз (я при переносе в `internal/telemetry` скопировал его ещё раз, а не воспользовался случаем объединить). Политика ротации у них осознанно разная и объединять её не нужно: `outbox.go` держит все пронумерованные файлы вечно и умеет их перечитать (нужно для восстановления `pending`-снапшотов после рестарта перед пушем во Flatline), `telemetry/writer.go` — write-only архив с zip-упаковкой и удалением исходника (перечитывать программно не нужно никогда). Но сам примитив "дозаписать байты в активный файл с fsync и отследить, не пора ли ротировать" — общий и не должен существовать в двух копиях.

- **Как поправлено:** вынесен общий примитив `internal/platform/appendfile.Append(path, data)` — create-if-missing + открыть на дозапись + `fsync` + закрыть. `outbox.go` и `telemetry/writer.go` по-прежнему сами решают, когда и как ротировать (нумерованно/по времени, с zip или без) и как отслеживать текущий размер (outbox — в памяти инкрементально, telemetry — пересканом директории) — это осталось разным осознанно, унифицировался только сам факт записи байт на диск.
- Добавлены тесты (`internal/platform/appendfile/appendfile_test.go`): создание файла и родительской директории, дозапись к существующему файлу, ошибка при пути-директории.
- `go build`/`go vet`/`go test ./...` — зелено, включая `internal/metrics` (Flatline outbox не сломан).

## Чек-лист по архитектурному самоаудиту

- ✅ Вынести общий append+rotate-size примитив, используемый `internal/telemetry/writer.go` и `internal/metrics/outbox.go`.
