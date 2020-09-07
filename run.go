package main // import "github.com/inclavare-containers/enclaved"

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"github.com/urfave/cli"
	pb "github.com/inclavare-containers/enclaved/proto"
	"google.golang.org/grpc"
	"log"
	"net"
	"time"
)

const (
	AgentRequestTypeEcho = iota
	AgentRequestTypeRemoteAttestation
)

const (
	nonceMaxLength = 16
)

var (
	agentSocketPath              = "/run/enclaved/agent.sock"
	listeningPort                = 50051
	agentSocketConnectionTimeout = 3 /* In second */
)

var (
	agentChannel *net.UnixConn
)

type server struct {
	pb.UnimplementedEnclavedServer
}

func connectAgent() (*net.UnixConn, error) {
	addr, err := net.ResolveUnixAddr("unix", agentSocketPath)
	if err != nil {
		return nil, err
	}

	conn, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

func (s *server) InitiateChallenge(ctx context.Context, in *pb.AttestChallenge) (*pb.AttestResponse, error) {
	for agentChannel == nil {
		conn, err := connectAgent()
		if err != nil {
			log.Println(err)
			log.Printf("Awaitting for %d seconds to re-establish the agent connection\n",
				agentSocketConnectionTimeout)
			time.Sleep(time.Duration(agentSocketConnectionTimeout) * time.Second)
			continue
		}

		agentChannel = conn
	}

	nonce := in.GetNonce()
	log.Printf("nonce %v received", nonce)
	if len(nonce) != nonceMaxLength {
		return nil, fmt.Errorf("Invalid length of nonce: %d", len(nonce))
	}

	req := uint32(AgentRequestTypeRemoteAttestation)
	sz := uint32(len(nonce))

	buf := bytes.NewBuffer([]byte{})
	binary.Write(buf, binary.LittleEndian, &req)
	binary.Write(buf, binary.LittleEndian, &sz)
	binary.Write(buf, binary.LittleEndian, &nonce)
	if _, err := agentChannel.Write(buf.Bytes()); err != nil {
		log.Println(err)
		agentChannel.Close()
		agentChannel = nil
		return s.InitiateChallenge(ctx, in)
	}

	rbuf := make([]byte, 8)
	if n, err := agentChannel.Read(rbuf); n != len(rbuf) || err != nil {
		log.Println(err)
		agentChannel.Close()
		agentChannel = nil
		return s.InitiateChallenge(ctx, in)
	}

	var status uint32
	buf = bytes.NewBuffer(rbuf[:4])
	binary.Read(buf, binary.LittleEndian, &status)

	var payloadLen uint32
	buf = bytes.NewBuffer(rbuf[4:])
	binary.Read(buf, binary.LittleEndian, &payloadLen)

	if status != 0 {
		return nil, fmt.Errorf("Response status %d\n", status)
	}

	payload := make([]byte, payloadLen)
	if n, err := agentChannel.Read(payload); uint32(n) != payloadLen || err != nil {
		log.Println(err)
		agentChannel.Close()
		agentChannel = nil
		return s.InitiateChallenge(ctx, in)
	}

	return &pb.AttestResponse{Quote: payload}, nil
}

var runCommand = cli.Command{
	Name:  "run",
	Usage: "run the enclaved",
	ArgsUsage: `[command options]

EXAMPLE:

       # shelterd-shim-agent run &`,
	Flags: []cli.Flag{
		cli.IntFlag{
			Name:        "port",
			Value:       listeningPort,
			Usage:       "listening port for receiving external requests",
			Destination: &listeningPort,
		},
		cli.IntFlag{
			Name:        "timeout",
			Value:       agentSocketConnectionTimeout,
			Usage:       "the timeout in second for re-establishing the connection to enclaved",
			Destination: &agentSocketConnectionTimeout,
		},
	},
	SkipArgReorder: true,
	Action: func(cliContext *cli.Context) error {
		conn, err := connectAgent()
		if err != nil {
			log.Println(err)
			return err
		}

		port := fmt.Sprintf(":%d", listeningPort)
		lis, err := net.Listen("tcp", port)
		if err != nil {
			conn.Close()
			log.Println(err)
			return err
		}

		s := grpc.NewServer()
		pb.RegisterEnclavedServer(s, &server{})

		agentChannel = conn

		if err := s.Serve(lis); err != nil {
			conn.Close()
			log.Println(err)
			return err
		}

		conn.Close()

		return nil
	},
}
