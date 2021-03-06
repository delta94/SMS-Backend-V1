package handler

import (
	"log"
	"os"
)

var naverClientID string
var naverClientSecret string
var consulIndexHeader string
var snsTopicArn string

func init() {
	if naverClientID = os.Getenv("NAVER_CLIENT_ID"); naverClientID == "" {
		log.Fatal("please set NAVER_CLIENT_ID in environment variable")
	}
	if naverClientSecret = os.Getenv("NAVER_CLIENT_SECRET"); naverClientSecret == "" {
		log.Fatal("please set NAVER_CLIENT_SECRET in environment variable")
	}
	if consulIndexHeader = os.Getenv("CONSUL_INDEX_HEADER"); consulIndexHeader == "" {
		log.Fatal("please set CONSUL_INDEX_HEADER in environment variable")
	}
	if snsTopicArn = os.Getenv("SNS_TOPIC_ARN"); snsTopicArn == "" {
		log.Fatal("please set SNS_TOPIC_ARN in environment variable")
	}
}

var limitTableForNaver = map[string]bool{}
