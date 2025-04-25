package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

// bu uyuglamayı docker üzerinden çalıştırmayı dene bunun için dockerfile oluştur.
// bu uyuglamayı redis ile beraber yönetmek için docker-compose oluştur.
type Weather struct {
	Latitude        float64 `json:"latitude"`
	Longitude       float64 `json:"longitude"`
	ResolvedAddress string  `json:"resolvedAddress"`
	Timezone        string  `json:"timezone"`
	Description     string  `json:"description"`
	Days            []Day   `json:"days"`
}

type Day struct {
	Datetime    string  `json:"datetime"`
	Temp        float64 `json:"temp"`
	FeelsLike   float64 `json:"feelslike"`
	WindSpeed   float64 `json:"windspeed"`
	Visibility  float64 `json:"visibility"`
	UVIndex     float64 `json:"uvindex"`
	Sunrise     string  `json:"sunrise"`
	Sunset      string  `json:"sunset"`
	Icon        string  `json:"icon"`
	Description string  `json:"description"`
}

func getIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		fmt.Println("Error parsing IP : %v", err)
		return ""
	}
	return host
}

func rateLimiterMiddleware(next http.HandlerFunc, limit rate.Limit, burst int) http.HandlerFunc {
	ipLimiterMap := make(map[string]*rate.Limiter)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getIP(r)

		limiter, exists := ipLimiterMap[ip]
		if !exists {
			limiter = rate.NewLimiter(limit, burst)
			ipLimiterMap[ip] = limiter
		}
		if !limiter.Allow() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "To many requests"})
			return
		}
		next.ServeHTTP(w, r)
	})

}

func redisMiddleware(redisDB *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		country := r.URL.Query().Get("country")
		val, err := getRedisValue(redisDB, country)
		if err != nil {
			key := os.Getenv("API_KEY")
			if key == "" {
				http.Error(w, "Api key could not found", http.StatusInternalServerError)
				return
			}
			weather, err := getWeatherValue(country, key)
			if err != nil {
				http.Error(w, "getWeatherValue Error : "+err.Error(), http.StatusInternalServerError)
				return
			}
			data, err := json.Marshal(weather)
			if err != nil {
				http.Error(w, "Error marshalling JSON", http.StatusInternalServerError)
				return
			}
			err = setRedisValue(redisDB, country, data, 5*time.Minute)
			if err != nil {
				http.Error(w, "Error setting value in Redis", http.StatusInternalServerError)
				return
			}
			fmt.Println("No redis")
			w.Write(data)
		} else {
			fmt.Println("Yes redis")
			w.Write([]byte(val))
		}
	}
}

func main() {
	var redisDB = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/weather", http.StatusOK)
	})
	http.HandleFunc("/weather", rateLimiterMiddleware(redisMiddleware(redisDB), rate.Limit(2), 10))

	http.ListenAndServe(":7878", nil)

}

func getWeatherValue(country string, key string) (Weather, error) {

	if key == "" {
		return Weather{}, fmt.Errorf("API key cannot be empty")
	}
	if country == "" {
		country = "maltepe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	url := "https://weather.visualcrossing.com/VisualCrossingWebServices/rest/services/timeline/" + country + "/today?unitGroup=metric&include=days&key=" + key + "&contentType=json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Weather{}, fmt.Errorf("failed to create HTTP request: %v", err)
	}

	client := &http.Client{}
	res, err := client.Do(req)

	if err != nil {
		return Weather{}, fmt.Errorf("failed to make HTTP request: %v", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return Weather{}, fmt.Errorf("failed to read response body: %v", err)
	}

	if res.StatusCode != http.StatusOK {
		return Weather{}, fmt.Errorf("status code : %d , message : %s", res.StatusCode, body)
	}

	var weather Weather
	err = json.Unmarshal(body, &weather)
	if err != nil {
		return Weather{}, err
	}
	return weather, nil
}

func setRedisValue(redisDB *redis.Client, key string, value interface{}, expiration time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := redisDB.Set(ctx, key, value, expiration).Err()
	if err != nil {
		return err
	}
	return nil
}

func getRedisValue(redisDB *redis.Client, key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	val, err := redisDB.Get(ctx, key).Result()
	if err != nil {
		return "", err
	}
	return val, nil
}
