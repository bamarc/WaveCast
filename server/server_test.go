package main

import (
	"context"
	"crypto/tls"
	"github.com/quic-go/quic-go"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConnectionEstablishment(t *testing.T) {
	go main()

	req, err := http.NewRequest("GET", "/sas/spec.json", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Create a recorder to capture the HTTP response
	recorder := httptest.NewRecorder()

	// Call the handler function
	handleSpecFileRequest(recorder, req)

	// Check the response status code
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, recorder.Code)
	}
}

func TestControlStreamData(t *testing.T) {
	go main()

	tlsConf := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"quic-echo-example"},
	}
	// setting up connection
	conn, err := quic.DialAddr(context.Background(), sasp_addr, tlsConf, nil)
	if err != nil {
		t.Error("Received error while establishing connection", err)
	}
	defer conn.CloseWithError(0, "")
	mediaStream, err := conn.OpenStreamSync(context.Background())
	if err != nil {
		t.Error("Received error while opening Streams", err)
	}
	// opening streams
	ctlStream, err := conn.OpenStreamSync(context.Background())
	if err != nil {
		t.Error("Received error while opening Streams", err)
	}
	//defer ctlStream.Close()
	defer mediaStream.Close()

	// try sending example payload

	InfoLog.Println("Trying first command")
	sendMessage(ctlStream, []byte("START"), "client:")
	answer, err := readCommandFromStream(ctlStream, "client:")
	InfoLog.Println("Received the following: ", string(answer))

	InfoLog.Println("Trying first command")
	sendMessage(ctlStream, []byte("STOP"), "client:")
	answer, err = readCommandFromStream(ctlStream, "client:")
	InfoLog.Println("Received the following: ", string(answer))

	sendMessage(ctlStream, []byte("NEXT"), "client:")
	answer, err = readCommandFromStream(ctlStream, "client:")
	InfoLog.Println("Received the following: ", string(answer))

	http.NewRequest("POST", "/admin/stop", nil)
}
