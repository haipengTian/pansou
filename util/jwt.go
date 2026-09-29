package util

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims JWT载荷结构
//
// Role 仅供客户端展示；服务端鉴权以账号存储中的当前角色为准。
// Version 对应账号的 TokenVersion，禁用、改密、改角色后旧令牌因版本不符而失效。
type Claims struct {
	Username string `json:"username"`
	Role     string `json:"role,omitempty"`
	Version  int    `json:"ver,omitempty"`
	jwt.RegisteredClaims
}

// GenerateToken 生成JWT token（不带角色与版本，保留给旧调用方）
func GenerateToken(username string, secret string, expiry time.Duration) (string, error) {
	return GenerateTokenFor(username, "", 0, secret, expiry)
}

// GenerateTokenFor 生成带角色与令牌版本的JWT token
func GenerateTokenFor(username, role string, version int, secret string, expiry time.Duration) (string, error) {
	if username == "" {
		return "", errors.New("username cannot be empty")
	}
	if secret == "" {
		return "", errors.New("secret cannot be empty")
	}

	expirationTime := time.Now().Add(expiry)
	claims := &Claims{
		Username: username,
		Role:     role,
		Version:  version,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expirationTime),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "pansou",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ValidateToken 验证JWT token
func ValidateToken(tokenString string, secret string) (*Claims, error) {
	if tokenString == "" {
		return nil, errors.New("token cannot be empty")
	}
	if secret == "" {
		return nil, errors.New("secret cannot be empty")
	}

	claims := &Claims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		// 验证签名算法
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})

	if err != nil {
		return nil, err
	}

	if !token.Valid {
		return nil, errors.New("invalid token")
	}

	return claims, nil
}
