package file_storage

import (
	tr "Awesome-DFS/protobuf/transfer"
	cf "Awesome-DFS/storage_node/chunk_forwarding"
	ms "Awesome-DFS/storage_node/metadata_service"
	val "Awesome-DFS/storage_node/storage_validation"
	"crypto/sha256"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

var mu sync.Mutex

type uploadServer struct {
	tr.UnimplementedFileTransferServer
}

func initChunk(meta *tr.MetaData) (*os.File, error) {
	dirPath, err := initDir(meta.FileUuid)
	if err != nil {
		return nil, err
	}

	filePath := fmt.Sprintf("%s/%s.chunk", dirPath, meta.UniqueName)
	file, err := os.Create(filePath)
	if err != nil {
		return nil, err
	}

	return file, nil
}

func initDir(name string) (string, error) {
	mu.Lock()
	defer mu.Unlock()

	fullPath := fmt.Sprintf("storage/%s", name)
	err := os.MkdirAll(fullPath, os.ModePerm)
	if err != nil {
		return "", err
	}

	return fullPath, nil
}

func (s *uploadServer) Upload(stream tr.FileTransfer_UploadServer) error {
	p, ok := peer.FromContext(stream.Context())
	if !ok {
		return fmt.Errorf("failed to get peer from context")
	}
	log.Printf("Received storage request from %s\n", p.Addr)

	hasher := sha256.New()
	start := time.Now()
	var chunkFile *os.File
	var metadata *tr.MetaData
	for {
		chunk, err := stream.Recv()

		if err == io.EOF {
			err = chunkFile.Sync()
			if err != nil {
				log.Printf("failed to sync chunk file: %v", err)
				return stream.SendAndClose(&tr.UploadResponse{
					Status:  tr.Status_STATUS_ERROR,
					Message: fmt.Sprintf("failed to sync chunk file: %v", err),
				})
			}

			elapsed := time.Since(start)

			checksum := fmt.Sprintf("%x", hasher.Sum(nil))
			ms.NewChunk(metadata.FileUuid, metadata.UniqueName, checksum)

			log.Printf("stored %s successfully in %v\n", metadata.UniqueName, elapsed)

			go val.ValidateChunk(metadata.FileUuid)

			go cf.Next(chunkFile, metadata)

			return stream.SendAndClose(&tr.UploadResponse{
				Status:  tr.Status_STATUS_OK,
				Message: fmt.Sprintf("Upload completed in %v", elapsed),
			})
		}

		if err != nil {
			log.Printf("failed to receive chunk metadata: %v", err)
			return fmt.Errorf("failed to receive chunk metadata: %v", err)
		}

		switch payload := chunk.Payload.(type) {
		case *tr.Chunk_Meta:
			metadata = payload.Meta
			chunkFile, err = initChunk(payload.Meta)
			if err != nil {
				log.Printf("failed to initialize chunk: %v", err)
				return fmt.Errorf("failed to initialize chunk: %v", err)
			}
		case *tr.Chunk_Data:
			_, err = chunkFile.Write(payload.Data.RawBytes)
			if err != nil {
				log.Printf("failed to write chunk: %v", err)
				return fmt.Errorf("failed to write chunk: %v", err)
			}

			_, err = hasher.Write(payload.Data.RawBytes)
			if err != nil {
				log.Printf("failed to write chunk to hasher: %v", err)
				return fmt.Errorf("failed to write chunk to hasher: %v", err)
			}
		}
	}
}

func RegisterFileTransferServer(server *grpc.Server) {
	tr.RegisterFileTransferServer(server, new(uploadServer))
}
