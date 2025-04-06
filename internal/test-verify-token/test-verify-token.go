package verifytoken

import (
	"encoding/json"
	"fmt"
	"net/http"
)

const checkTokenURL = "http://77.222.53.150/api/check_token/"

type TokenResponse struct {
	Success string `json:"success"`
}

func ValidateToken(token string) (bool, error) {
	req, err := http.NewRequest("GET", checkTokenURL, nil)
	if err != nil {
		return false, fmt.Errorf("ошибка при создании запроса: %w", err)
	}

	req.Header.Set("Authorization", token)
	req.Header.Set("User-Agent", "Go-Client/1.0")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("ошибка при выполнении запроса: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("неожиданный статус: %s", resp.Status)
	}

	var result TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("ошибка декодирования JSON: %w", err)
	}

	// Проверяем, success == "ok"
	return result.Success == "ok", nil
}

// func main() {
// 	token := "Token 4b4d65e2c6987c60be6231febe98a064b7167ae4" // замените на реальный токен
// 	valid, err := ValidateToken(token)
// 	if err != nil {
// 		fmt.Println("Ошибка валидации:", err)
// 		return
// 	}

// 	fmt.Println(valid)
// }
