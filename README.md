# pdfFiller API

Cookie-сессии, пользователи в Postgres и одна генерация PDF на оплату из ChatPlace.

## Локальный запуск

Из этой папки:

```bash
docker compose up --build
```

API: `http://localhost:8080`

Postgres: `localhost:5432`, база `pdffiller`, пользователь и пароль `pdffiller`.

Локальный тестовый аккаунт создаётся при старте, если заданы `SEED_LOGIN` и `SEED_PASSWORD`. В compose это `anna` / `password1`, с 5 генерациями. Для настоящей оплаты уберите seed.

Фронт на `http://localhost:5173` проксирует вход, выход и списание генерации на этот API. После запуска войдите заново: старая сессия из браузера не является cookie этого сервера.

## Оплата ChatPlace

`POST /api/v1/payments/chatplace`

Заголовок: `X-Webhook-Secret: <CHATPLACE_WEBHOOK_SECRET>`

```json
{
  "paymentId": "уникальный id платежа",
  "instagramUsername": "username"
}
```

Если пользователя ещё нет, API создаёт логин из Instagram-имени и один раз возвращает пароль. Каждая новая оплата добавляет одну генерацию и увеличивает `chatplacePayments`. Повтор того же `paymentId` генерацию не добавляет.

Скачивание PDF вызывает `POST /api/v1/documents/consume` и списывает одну генерацию. Когда их ноль, скачивание блокируется до следующей оплаты.

Пароли в базе хранятся только как bcrypt-хеш.
