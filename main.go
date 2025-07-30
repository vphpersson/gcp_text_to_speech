package main

import (
	"context"
	"errors"
	"fmt"
	motmedelContext "github.com/Motmedel/utils_go/pkg/context"
	motmedelErrors "github.com/Motmedel/utils_go/pkg/errors"
	"github.com/Motmedel/utils_go/pkg/log/error_logger"
	"github.com/vphpersson/argument_parser/pkg/argument_parser"
	"github.com/vphpersson/argument_parser/pkg/types/option"
	"golang.org/x/sync/errgroup"
	"io"
	"log/slog"
	"os"
	"strings"

	texttospeech "cloud.google.com/go/texttospeech/apiv1"
	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

var (
	ErrNilFile           = errors.New("nil file")
	ErrEmptyVoice        = errors.New("empty voice")
	ErrEmptyLanguageCode = errors.New("empty language code")
)

func synthesizeChunks(
	ctx context.Context,
	chunks []string,
	voice string,
	languageCode string,
) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if voice == "" {
		return nil, motmedelErrors.NewWithTrace(ErrEmptyVoice)
	}

	if languageCode == "" {
		return nil, motmedelErrors.NewWithTrace(ErrEmptyLanguageCode)
	}

	if len(chunks) == 0 {
		return nil, nil
	}

	client, err := texttospeech.NewClient(ctx)
	if err != nil {
		return nil, motmedelErrors.NewWithTrace(fmt.Errorf("text to speech new client: %w", err))
	}
	defer func() {
		if err := client.Close(); err != nil {
			slog.ErrorContext(
				motmedelContext.WithErrorContextValue(ctx, fmt.Errorf("text to speech client close: %w", err)),
				"An error occurred when closing the TTS client.",
			)
		}
	}()

	chunkMap := make([][]byte, len(chunks))

	errGroup, errGroupCtx := errgroup.WithContext(ctx)

	for i, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			break
		}

		errGroup.Go(
			func() error {
				request := &texttospeechpb.SynthesizeSpeechRequest{
					Input: &texttospeechpb.SynthesisInput{
						InputSource: &texttospeechpb.SynthesisInput_Text{Text: chunk},
					},
					Voice: &texttospeechpb.VoiceSelectionParams{
						LanguageCode: languageCode,
						Name:         voice,
					},
					AudioConfig: &texttospeechpb.AudioConfig{
						AudioEncoding: texttospeechpb.AudioEncoding_MP3,
					},
				}

				response, err := client.SynthesizeSpeech(errGroupCtx, request)
				if err != nil {
					return motmedelErrors.NewWithTrace(
						fmt.Errorf("text to speech synthesize speech: %w", err),
						chunk, languageCode, voice,
					)
				}

				chunkMap[i] = response.AudioContent

				slog.Info(fmt.Sprintf("Sythesized chunk #%d.", i))

				return nil
			},
		)
	}

	if err := errGroup.Wait(); err != nil {
		return nil, fmt.Errorf("err group wait: %w", err)
	}

	return chunkMap, nil
}

func chunkText(input string, size int) []string {
	var chunks []string
	for len(input) > size {
		split := strings.LastIndex(input[:size], " ")
		if split == -1 {
			split = size
		}
		chunks = append(chunks, strings.TrimSpace(input[:split]))
		input = strings.TrimSpace(input[split:])
	}
	if len(input) > 0 {
		chunks = append(chunks, input)
	}
	return chunks
}

func makeChunks(textFile *os.File) ([]string, error) {
	if textFile == nil {
		return nil, motmedelErrors.NewWithTrace(ErrNilFile)
	}

	textBytes, err := io.ReadAll(textFile)
	if err != nil {
		return nil, motmedelErrors.NewWithTrace(fmt.Errorf("io read all: %w", err))
	}

	return chunkText(string(textBytes), 4500), nil
}

func main() {
	logger := error_logger.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger.Logger)

	var textFile os.File
	var outFile os.File
	voice := "en-US-Chirp3-HD-Orus"
	languageCode := "en-US"

	argumentParser := argument_parser.ArgumentParser{
		Options: []option.Option{
			option.NewFileOption(
				't',
				"text",
				"The path of a file of text.",
				true,
				&textFile,
			),
			option.NewFileOptionExtra(
				'o',
				"out",
				"The path of the output file.",
				true,
				os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
				0600,
				&outFile,
			),
			option.NewStringOption(
				'v',
				"voice",
				"The voice to use.",
				false,
				&voice,
			),
			option.NewStringOption(
				'l',
				"language-code",
				"The language code of the voice.",
				false,
				&languageCode,
			),
		},
	}

	if err := argumentParser.Parse(); err != nil {
		logger.FatalWithExitingMessage("An error occurred when parsing the arguments.", err)
	}

	defer func() {
		if err := textFile.Close(); err != nil {
			logger.Error("An error occurred when closing the text file.", err)
		}
	}()

	defer func() {
		if err := outFile.Close(); err != nil {
			logger.Error("An error occurred when closing the output file.", err)
		}
	}()

	// TODO: The chunks need extra processing to not hit the 900-byte sentence limit.

	chunks, err := makeChunks(&textFile)
	if err != nil {
		logger.FatalWithExitingMessage("An error occurred when making chunks.", err)
	}

	logger.Info(fmt.Sprintf("Created %d chunks.", len(chunks)))

	chunkMap, err := synthesizeChunks(context.Background(), chunks, voice, languageCode)
	if err != nil {
		logger.FatalWithExitingMessage("An error occurred when synthesizing chunks.", err)
	}

	for _, chunk := range chunkMap {
		if _, err := outFile.Write(chunk); err != nil {
			logger.Error("An error occurred when writing a chunk to the out file.", err)
		}
	}
}
