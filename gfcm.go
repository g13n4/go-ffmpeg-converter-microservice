package goffmpegconvertermicroservice

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"

	"google.golang.org/grpc"

	pb "github.com/g13n4/go-ffmpeg-converter-microservice/video-converter"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

var FILE_HANDLER_MAP = map[int64]*os.File{}

type videoConverterServer struct {
	pb.UnimplementedVideoConverterServer
	videoToConvertChunks []*pb.VideoToConvert
	convertedVideoChunks []*pb.ConvertedVideo
}

func WriteBytesToFile(fileName string, videoId int64, data []byte) error {
	// Check if file exists. If it does we append bytes and if not we create a new one
	handler, ok := FILE_HANDLER_MAP[videoId]

	if ok {
		_, handlerErr := handler.Write(data)
		return handlerErr
	} else {
		file, err := os.OpenFile(fileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

		if err != nil {
			return err
		}

		_, writeErr := file.Write(data)
		if writeErr == nil {
			FILE_HANDLER_MAP[videoId] = file
			return nil
		}
		return writeErr
	}

}

func TransformVideo(fileInput string, fileOutput string, fileKwargs map[string]interface{}) error {
	err := ffmpeg.Input(fileInput).
		Output(fileOutput, fileKwargs).
		OverWriteOutput().ErrorToStdOut().Run()

	return err
}

func (vcs *videoConverterServer) ConvertVideo(stream pb.VideoConverter_ConvertVideoServer) error {
	for {
		in, err := stream.Recv()

		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// if it's not the first chuck and the handler is not present in the map then there was an error
		// and we can safely skip next chunks

		vi := in.VideoInfo
		_, ok := FILE_HANDLER_MAP[vi.VideoId]
		if in.ChunkIndex != 1 && !ok {
			errorMessage := "Redundant Error"
			sendError := stream.Send(&pb.ConvertedVideo{
				Sucess: false,
				Error:  &errorMessage,
			})
			if sendError != nil {
				return sendError
			}
		}

		fileNameInput := fmt.Sprintf("/tmp/%d/input/%s.%s", vi.VideoId, vi.FileEncoding)
		fileNameOutput := fmt.Sprintf("/tmp/%d/output/%s.%s", vi.VideoId, vi.OutputEncoding)

		writingErr := WriteBytesToFile(fileNameInput, vi.VideoId, in.VideoFeed)

		// something went wrong so we remove the file from our file system
		errorsList := []string{}
		if writingErr != nil {
			errorsList = append(errorsList, writingErr.Error())
			handler, ok := FILE_HANDLER_MAP[vi.VideoId] // check if file exists. If it does we remove it
			if ok {
				delete(FILE_HANDLER_MAP, vi.VideoId)
			}
			closeError := handler.Close()
			if closeError != nil {
				errorsList = append(errorsList, closeError.Error())
			}

			_, fileStatsErr := os.Stat(fileNameInput)

			if errors.Is(fileStatsErr, os.ErrNotExist) {
				removeFileError := os.Remove(fileNameInput)

				if removeFileError != nil {
					errorsList = append(errorsList, removeFileError.Error())
				}
			}
		}

		// everything is great. Time to transform it
		if in.LastChuck {
			transformationError := TransformVideo(fileNameInput, fileNameOutput, in.ConvertionSettings)

			// ready to send it back
			if transformationError == nil {
				outputFile, outputFileErr := os.OpenFile(fileNameOutput, os.O_RDONLY|os.O_WRONLY, 0644)

				if outputFileErr != nil {
					errorsList = append(errorsList, outputFileErr.Error())
				} else {
					readBuffer := make([]byte, 0, 1024*4)
					bufReader := bufio.NewReader(outputFile)
					for {
						n, BRerr := bufReader.Read(readBuffer[:cap(readBuffer)])
						readBuffer = readBuffer[:n] // take only existing bytes
						if n == 0 {
							if BRerr == nil {
								continue
							}
							if BRerr == io.EOF {
								break
							}
						}

						if BRerr != nil && BRerr != io.EOF {
							errorsList = append(errorsList, BRerr.Error())
							break
						}

						sendError := stream.Send(&pb.ConvertedVideo{
							Sucess:    false,
							VideoFeed: readBuffer,
						})

						if sendError != nil {
							errorsList = append(errorsList, sendError.Error())
							break
						}

					}
				}

			}
		}

		// return a concataned list of errors
		if len(errorsList) > 0 {
			errorMessages := strings.Join(errorsList, " Error: ")
			sendError := stream.Send(&pb.ConvertedVideo{
				Sucess: false,
				Error:  &errorMessages,
			})
			if sendError != nil {
				return sendError
			}
		}
	}
}

func main() {
	port := os.Getenv("GRPC_PORT")
	if len(port) == 0 {
		port = "50051"
	}

	lis, err := net.Listen("tcp", port)

	if err != nil {
		log.Fatalf("Cannot create an instance of Listen: %s", err)
	}
	grpcService := grpc.NewServer()

	pb.RegisterVideoConverterServer(grpcService, &videoConverterServer{})
	grpcService.Serve(lis)
}
