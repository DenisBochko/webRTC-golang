# webRTC сервер

Логика:
1) Дёргаем ручку создания комнаты, в теле указываете название и пароль для подключения 
Ручка возвращает ссылку для подключения, где пользователь уже и введёт имя и подключится к митингу
2) Каждый пользователь при подключении проходит автоизацию по токену (авторизация происходит на стороне сервера)
при перенаправлении устанавливаем пользователю заголовок с хеддером:
Authorization: Token 4b4d65e2c6987c60be6231febe98a064b7167ae4

- Создание комнаты
```curl
curl -X POST https://3449009-eq23140.twc1.net/api/create-room \
  -H "Content-Type: application/json" \
  -d '{"name":"myroom", "password":"secret123"}'

{
    "password": "secret123",
    "room": "myroom",
    "status": "success",
    "uri": "https://3449009-eq23140.twc1.net/?room=myroom"
}
```


- Проверка комнаты
```
curl -X POST http://localhost:8080/api/check-room \
  -H "Content-Type: application/json" \
  -d '{"name":"myroom", "password":"secret123", "username":"user1"}'

{
    "room": "MytestRoom",
    "status": "success"
}

- Во время запроса на подключение угазываем в заголовке токен авторизованного пользователя
Authorization: Token 4b4d65e2c6987c60be6231febe98a064b7167ae4
```

