Membuat base project Golang yang testable dan siap untuk CI/CD membutuhkan struktur yang modular. Kunci utama agar unit test mudah diimplementasikan adalah dengan menggunakan Clean Architecture dan Dependency Injection melalui Interfaces.

Berikut adalah rencana komprehensif untuk membangun base backend project tersebut.

1. Struktur Direktori Utama
Gunakan adaptasi dari Standard Go Project Layout. Struktur ini memisahkan konfigurasi, logika bisnis, dan entry point aplikasi.

Plaintext
my-go-backend/
├── .github/
│   └── workflows/
│       └── ci.yml             # Script CI/CD GitHub Actions
├── cmd/
│   └── api/
│       └── main.go            # Entry point aplikasi (wiring dependencies)
├── internal/
│   ├── config/                # Load environment variables (.env)
│   ├── domain/                # Structs/Models dan Interfaces (Core)
│   ├── handler/               # HTTP Delivery (REST API / GraphQL)
│   ├── repository/            # Implementasi Database (Postgres, MySQL)
│   └── usecase/               # Business logic
├── pkg/
│   └── logger/                # Utility yang bisa dipakai ulang (custom logger, dll)
├── go.mod
├── go.sum
└── Makefile                   # Kumpulan script untuk run, test, build
2. Strategi Arsitektur & Kemudahan Unit Test
Agar mudah dites tanpa harus terhubung ke database asli, setiap layer harus bergantung pada Interface, bukan implementasi langsung.

Domain Layer (internal/domain): Definisikan model data dan interface untuk Repository & Usecase di sini.

Usecase Layer (internal/usecase): Berisi logika bisnis. Menerima Repository Interface via parameter (Dependency Injection).

Handler Layer (internal/handler): Menangani request/response HTTP. Menerima Usecase Interface.

Rekomendasi Tools Testing:

Standar Library: testing (Go bawaan).

Assertions & Mocking: [github.com/stretchr/testify](https://github.com/stretchr/testify) (mempermudah validasi assert.Equal).

Auto-Mock Generator: [github.com/vektra/mockery](https://github.com/vektra/mockery) (untuk men-generate file mock dari interface secara otomatis).

3. Script CI/CD Pipeline (GitHub Actions)
Gunakan GitHub Actions untuk otomatisasi Linting, Testing, dan Building setiap kali ada Push atau Pull Request ke branch main.

Buat file di .github/workflows/ci.yml:

YAML
name: Go Backend CI

on:
  push:
    branches: [ "main" ]
  pull_request:
    branches: [ "main" ]

jobs:
  build-and-test:
    runs-on: ubuntu-latest
    steps:
    - name: Checkout Code
      uses: actions/checkout@v4

    - name: Set up Go
      uses: actions/setup-go@v5
      with:
        go-version: '1.22'
        cache: true

    - name: Install Dependencies
      run: go mod download

    - name: Run Linter
      uses: golangci/golangci-lint-action@v3
      with:
        version: latest
        args: --timeout=5m

    - name: Run Unit Tests
      run: go test -v -coverprofile=coverage.out ./...

    - name: Check Test Coverage
      run: go tool cover -func=coverage.out

    - name: Build Application
      run: go build -v -o ./bin/api ./cmd/api/main.go
4. Makefile untuk Produktivitas
Sebagai pengguna lingkungan berbasis Linux/WSL, sebuah Makefile akan sangat mempercepat alur kerja harian. Buat file Makefile di root directory:

Makefile
.PHONY: run test mock lint build clean

run:
	go run cmd/api/main.go

test:
	go test -v -cover ./...

# Perintah untuk men-generate mock dari interface menggunakan mockery
mock:
	mockery --all --keeptree

lint:
	golangci-lint run

build:
	go build -o bin/api cmd/api/main.go

clean:
	rm -rf bin/
5. Langkah Eksekusi Pertama (Setup)
Jalankan urutan perintah ini di terminal untuk menginisialisasi proyek:

Inisialisasi Module:

Bash
mkdir my-go-backend && cd my-go-backend
go mod init github.com/username/my-go-backend
Install Framework & Tools Dasar:

Bash
# Contoh menggunakan router Chi (ringan, standar library compliant) dan godotenv
go get github.com/go-chi/chi/v5
go get github.com/joho/godotenv

# Install Testify untuk testing
go get -u github.com/stretchr/testify
Install Mockery (untuk generate mock):

Bash
go install github.com/vektra/mockery/v2@v2.42.1

Berikut adalah contoh implementasi lengkap untuk satu endpoint (GET /users/{id}).

Contoh ini menggunakan arsitektur yang kita bahas sebelumnya dan menunjukkan bagaimana Dependency Injection membuat mocking menjadi sangat mudah di level Usecase.

1. Layer Domain (internal/domain/user.go)
Di sini kita mendefinisikan struct (model data) dan interface (kontrak kerja).

Go
package domain

// Model representasi data
type User struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Interface untuk Repository (Berhubungan dengan Database)
type UserRepository interface {
	GetByID(id int) (*User, error)
}

// Interface untuk Usecase (Berhubungan dengan Business Logic)
type UserUsecase interface {
	GetUser(id int) (*User, error)
}
2. Layer Repository (internal/repository/user_repository.go)
Ini adalah implementasi asli yang akan terhubung ke database. Saat melakukan unit test pada Usecase, layer ini tidak akan dipakai, melainkan akan di-mock.

Go
package repository

import (
	"errors"
	"my-go-backend/internal/domain"
)

type userRepository struct {
	// Di aplikasi nyata, Anda akan meng-inject koneksi DB (misal *sql.DB atau GORM) di sini
}

func NewUserRepository() domain.UserRepository {
	return &userRepository{}
}

func (r *userRepository) GetByID(id int) (*domain.User, error) {
	// DUMMY IMPLEMENTATION: Simulasi query ke database
	if id == 1 {
		return &domain.User{ID: 1, Name: "Developer", Email: "dev@example.com"}, nil
	}
	return nil, errors.New("user not found")
}
3. Layer Usecase (internal/usecase/user_usecase.go)
Usecase berisi logika bisnis. Perhatikan bagaimana userUsecase menerima domain.UserRepository melalui fungsinya, bukan menginisialisasi database secara langsung. Inilah yang disebut Dependency Injection.

Go
package usecase

import (
	"errors"
	"my-go-backend/internal/domain"
)

type userUsecase struct {
	repo domain.UserRepository
}

// Inject dependency melalui parameter
func NewUserUsecase(repo domain.UserRepository) domain.UserUsecase {
	return &userUsecase{
		repo: repo,
	}
}

func (u *userUsecase) GetUser(id int) (*domain.User, error) {
	// Logika bisnis: ID tidak boleh <= 0
	if id <= 0 {
		return nil, errors.New("invalid user ID")
	}

	// Memanggil repository (bisa DB asli, bisa mock saat test)
	return u.repo.GetByID(id)
}
4. Layer Handler (internal/handler/user_handler.go)
Handler bertugas menerima request HTTP dan mengembalikan response. (Contoh ini menggunakan framework go-chi/chi).

Go
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"my-go-backend/internal/domain"

	"github.com/go-chi/chi/v5"
)

type UserHandler struct {
	usecase domain.UserUsecase
}

func NewUserHandler(r chi.Router, us domain.UserUsecase) {
	handler := &UserHandler{usecase: us}
	// Mendaftarkan endpoint ke router
	r.Get("/users/{id}", handler.GetByID)
}

func (h *UserHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid ID format", http.StatusBadRequest)
		return
	}

	user, err := h.usecase.GetUser(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}
5. Unit Test Usecase dengan Mock (internal/usecase/user_usecase_test.go)
Inilah letak magic-nya. Kita akan menguji user_usecase.go tanpa perlu terhubung ke database asli, melainkan menggunakan data pura-pura (mock) lewat library testify.