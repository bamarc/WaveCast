package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"embed"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
)

var (
	WarningLog    *log.Logger
	InfoLog       *log.Logger
	ErrorLog      *log.Logger
	AppContext, _ = context.WithCancel(context.Background())
)

const http_addr = "localhost:4432"
const sasp_addr = "localhost:4433"

type SASPConnection struct {
	Connection    *quic.Connection
	ControlStream *quic.Stream
	MediaStream   *quic.Stream
}

func init() {
	WarningLog = log.New(os.Stdout, "WARNING: ", log.Ldate|log.Ltime|log.Lshortfile)
	InfoLog = log.New(os.Stdout, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
	ErrorLog = log.New(os.Stderr, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
}

// We start a server echoing data on the first stream the client opens,
// then connect with a client, send the message, and wait for its receipt.
func main() {
	// start http/3 server for meta requests
	go func() {
		_ = setUpMetaServe()
	}()
	// start quic listener to establish SASP server
	go func() {
		_ = setUpCoSASP()
	}()

	<-AppContext.Done()
}

func setUpMetaServe() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/sas/spec.json", handleSpecFileRequest) // Add handler for spec file	if err != nil
	mux.HandleFunc("/admin/stop", handleShutdownRequest)
	tlsConfig := generateTLSConfig()

	server := &http3.Server{
		Addr:      ":4432", // Oder ein anderer Port
		Handler:   mux,
		TLSConfig: http3.ConfigureTLSConfig(tlsConfig),
	}
	err := server.ListenAndServe()
	return err
}

//go:embed resources/*
var resources embed.FS

func handleSpecFileRequest(w http.ResponseWriter, r *http.Request) {
	// Read the embedded spec file
	specJSON, err := resources.ReadFile("resources/sas_spec.json")
	if err != nil {
		http.Error(w, "Error reading spec file", http.StatusInternalServerError)
		return
	}

	// Set appropriate headers
	w.Header().Set("Content-Type", "application/json")

	// Send the JSON response
	_, err = w.Write(specJSON)
	if err != nil {
		log.Println("Error sending spec file:", err)
	}
}

func handleShutdownRequest(w http.ResponseWriter, r *http.Request) {
	// Set appropriate headers
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	AppContext.Done()
}

func setUpCoSASP() error {
	listener, err := quic.ListenAddr(sasp_addr, generateTLSConfig(), nil)
	if err != nil {
		return err
	}
	defer listener.Close()

	for {
		conn, err := listener.Accept(context.Background())
		if err != nil {
			ErrorLog.Println("Failed to accept connection", err)
		}
		go func() {
			defer conn.CloseWithError(0, "")
			_ = handleConnection(conn)
		}()
	}
}

func handleConnection(connection quic.Connection) error {
	mediaStream, err := connection.AcceptStream(context.Background())
	if err != nil {
		return err
	}

	ctlStream, err := connection.AcceptStream(context.Background())
	if err != nil {
		return err
	}

	go func() {
		defer ctlStream.Close()
		defer mediaStream.Close()
		handleStreams(SASPConnection{Connection: &connection, ControlStream: &ctlStream, MediaStream: &mediaStream})
	}()

	<-connection.Context().Done()
	return nil
}

func handleStreams(conn SASPConnection) {
	// Control stream reading loop
	for {
		commandBytes, err := readCommandFromStream(*conn.ControlStream, "server:")
		if err != nil {
			ErrorLog.Println("Error reading control stream:", err)
			continue
		}
		// Process the received command
		answer := processCommand(commandBytes)
		_ = sendMessage(*conn.ControlStream, answer, "server:")
	}

}

func sendMessage(stream quic.Stream, message []byte, id string) error {
	// Get length of answer
	messageLen := len(message)
	InfoLog.Println(id, "Message length is ", messageLen)

	// Convert int (length) to []byte
	payloadLen := make([]byte, 2) // 2 bytes for uint16
	binary.BigEndian.PutUint16(payloadLen, uint16(messageLen))

	// Send stream (length first, then payload)
	InfoLog.Printf("%v Writing payload length %v to stream %v", id, payloadLen, stream.StreamID())
	_, err := stream.Write(payloadLen)
	if err != nil {
		ErrorLog.Println(id, "Error writing payload length:", err)
		return err
	}
	InfoLog.Println(id, "Writing message ", message)
	_, err = stream.Write([]byte(message))
	if err != nil {
		ErrorLog.Println(id, "Error writing answer:", err)
		return err
	}
	return nil
}

func readCommandFromStream(stream quic.Stream, id string) ([]byte, error) {
	// Read the length of the message (2 bytes)
	lengthBytes := make([]byte, 2)
	InfoLog.Println(id, "Reading length bytes from stream ", stream.StreamID())
	totalRead := 0
	for totalRead < 2 {
		n, err := io.ReadFull(stream, lengthBytes[totalRead:])
		if err != nil {
			if err == io.EOF {
				// Client closed the stream gracefully
				return nil, nil
			}
			ErrorLog.Println(id, "Error reading control stream length:", err)
			return nil, err // Or handle the error more gracefully
		}
		totalRead += n
	}

	InfoLog.Println(id, "Received length bytes :", lengthBytes)
	// Convert the length bytes to an integer
	messageLength := binary.BigEndian.Uint16(lengthBytes)

	InfoLog.Printf("%v Trying to read %v bytes from stream %v ", id, messageLength, stream.StreamID())
	// Read the command message
	commandBytes := make([]byte, messageLength)
	totalRead = 0
	for totalRead < int(messageLength) {
		n, err := io.ReadFull(stream, commandBytes[totalRead:])
		if err != nil {
			ErrorLog.Println(id, "Error reading command message:", err)
		}
		totalRead += n
	}

	return commandBytes, nil
}

func processCommand(command []byte) []byte {
	InfoLog.Println("Processing command:", string(command))
	switch string(command) {
	case "START":
		return []byte("OK")
	case "STOP":
		return []byte("OK, BYE")
	default:
		return []byte("NOT SUPPORTED")
	}
}

// A wrapper for io.Writer that also logs the message.
type loggingWriter struct{ io.Writer }

func (w loggingWriter) Write(b []byte) (int, error) {
	fmt.Printf("Server: Got '%s'\n", string(b))
	return w.Writer.Write(b)
}

// Setup a bare-bones TLS config for the server
func generateTLSConfig() *tls.Config {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		panic(err)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"quic-echo-example"},
	}
}
