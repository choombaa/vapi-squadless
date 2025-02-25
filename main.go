package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/joho/godotenv"
	"github.com/sashabaranov/go-openai"
)

// Global OpenAI client
var oai *openai.Client

// Contains potential arguments for a VAPI tool call
type VAPIFunctionArguments struct {
	Assistant *string `json:"assistant,omitempty"` // The description of the assistant to create
}

// Contains the name of a VAPI tool call and its arguments
type VAPIFunction struct {
	Name      string                `json:"name"`
	Arguments VAPIFunctionArguments `json:"arguments"`
}

// VAPI Tool Call information
type VAPIToolCall struct {
	// Call ID
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function VAPIFunction `json:"function"`
}

// VAPICustomer information
type VAPICustomer struct {
	Name   *string `json:"name,omitempty"`
	Number *string `json:"number,omitempty"`
}

// VAPIPhoneCall information
type VAPIPhoneCall struct {
	ID          string  `json:"id"`
	AssistantID *string `json:"assistantId"`
}

// VAPIWebhookMessage structure
type VAPIWebhookMessage struct {
	Type      string         `json:"type"`
	ToolCalls []VAPIToolCall `json:"toolCalls"`
	Call      *VAPIPhoneCall `json:"call"`
	Customer  *VAPICustomer  `json:"customer"`
}

type VAPIWebhookPayload struct {
	Message VAPIWebhookMessage `json:"message"`
}

type VAPIAssistant struct {
	Name             string                 `json:"name"`
	Model            map[string]interface{} `json:"model"`
	Voice            map[string]interface{} `json:"voice"`
	Transcriber      map[string]interface{} `json:"transcriber"`
	FirstMessage     *string                `json:"firstMessage,omitempty"`
	FirstMessageMode *string                `json:"firstMessageMode,omitempty"`
}

type RequestAssistantResponse struct {
	Assistant *VAPIAssistant `json:"assistant,omitempty"`
}

// Global cache to store phone number to assistant description mapping
// Use a database or Redis or something to store this
var phoneToAssistantCache = make(map[string]string)

// Helper function to get or create assistant based on phone number
func getOrCreateAssistant(callerPhoneNumber, vapiPhoneNumber string) VAPIAssistant {
	// Check if we have a cached assistant description
	if description, exists := phoneToAssistantCache[callerPhoneNumber]; exists {
		fmt.Println("found cached assistant description")
		fmt.Println(description)
		// Create a chat completion request to generate the prompt
		oaiMessages := []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleSystem,
				Content: "You are a prompt engineer. Generate a system prompt for an AI assistant based on this description. The prompt should be concise and clear.",
			},
			{
				Role:    openai.ChatMessageRoleUser,
				Content: description,
			},
		}

		response, err := oai.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
			Model:    "gpt-4o-mini",
			Messages: oaiMessages,
		})

		fmt.Printf("response: %+v\n", response.Choices[0].Message.Content)

		if err != nil {
			log.Printf("Failed to generate prompt: %v", err)
			return createInitialAssistant(vapiPhoneNumber)
		}

		// Get the generated prompt and append transfer instructions
		prompt := response.Choices[0].Message.Content + fmt.Sprintf(`
		For your first message, introduce yourself and state your purpose. Be concise and clear.
		Then, the caller will describe an assistant that they want to speak to. Call the createAssistant tool with the description.
		After the createAssistant tool call is successful, transfer the caller to %s.
		`, vapiPhoneNumber)

		firstMessageMode := "assistant-speaks-first-with-model-generated-message"
		return VAPIAssistant{
			Name: "CustomAssistant",
			Model: map[string]interface{}{
				"model":    "gpt-4o-mini",
				"provider": "openai",
				"messages": []interface{}{
					map[string]interface{}{
						"role":    "system",
						"content": prompt,
					},
				},
				"toolIds": []string{"d137092e-0250-4151-abf0-205ff2b0a438"},
				"tools": []map[string]interface{}{
					{
						"type": "transferCall",
						"destinations": []map[string]interface{}{
							{
								"type":   "number",
								"number": vapiPhoneNumber,
							},
						},
					},
				},
			},
			Voice: map[string]interface{}{
				"model":    "sonic-english",
				"voiceId":  "248be419-c632-4f23-adf1-5324ed7dbf1d",
				"provider": "cartesia",
			},
			Transcriber: map[string]interface{}{
				"model":    "general",
				"language": "en",
				"provider": "deepgram",
			},
			FirstMessageMode: &firstMessageMode,
		}
	}

	// If no cached assistant, return the default one
	fmt.Printf("no cached assistant, creating initial assistant")
	return createInitialAssistant(vapiPhoneNumber)
}

// Create an initial assistant that will handle the start of the call
func createInitialAssistant(phoneNumber string) VAPIAssistant {
	prompt := fmt.Sprintf(`You are a helpful assistant. Greet the caller and ask how you can help them today. They will tell you an agent that they want to speak to.
	After the createAssistant tool call is successful, transfer the caller to %s. Don't transfer the call until the createAssistant tool call is successful.
	`, phoneNumber)
	firstMessage := "Hello! How can I assist you today?"
	firstMessageMode := "assistant-speaks-first"

	return VAPIAssistant{
		Name: "AssistantGenerator",
		Model: map[string]interface{}{
			"model":    "gpt-4o-mini",
			"provider": "openai",
			"messages": []interface{}{
				map[string]interface{}{
					"role":    "system",
					"content": prompt,
				},
			},
			// createAssistant tool
			"toolIds": []string{"d137092e-0250-4151-abf0-205ff2b0a438"},
			// transfer the call back to itself to be re-routed
			"tools": []map[string]interface{}{
				{
					"type": "transferCall",
					"destinations": []map[string]interface{}{
						{
							"type":   "number",
							"number": phoneNumber,
						},
					},
				},
			},
		},
		Voice: map[string]interface{}{
			"model":    "sonic-english",
			"voiceId":  "248be419-c632-4f23-adf1-5324ed7dbf1d",
			"provider": "cartesia",
		},
		Transcriber: map[string]interface{}{
			"model":    "general",
			"language": "en",
			"provider": "deepgram",
		},
		FirstMessage:     &firstMessage,
		FirstMessageMode: &firstMessageMode,
	}
}

// Handle VAPI webhook calls
func handleVAPIWebhook(c *fiber.Ctx) error {
	vapiPhoneNumber := os.Getenv("VAPI_PHONE_NUMBER")

	// Parse the webhook payload
	var payload VAPIWebhookPayload
	if err := c.BodyParser(&payload); err != nil {
		log.Printf("Failed to parse webhook payload: %v", err)
		return c.SendStatus(400)
	}

	// Handle assistant request
	if payload.Message.Type == "assistant-request" {
		phoneNumber := ""
		if payload.Message.Customer != nil && payload.Message.Customer.Number != nil {
			phoneNumber = *payload.Message.Customer.Number
		}

		log.Printf("Received new call from: %v", phoneNumber)
		log.Printf("Call ID: %v", payload.Message.Call.ID)

		// Get or create assistant based on phone number
		assistant := getOrCreateAssistant(phoneNumber, vapiPhoneNumber)

		// Create response
		response := RequestAssistantResponse{
			Assistant: &assistant,
		}

		return c.JSON(response)
	}

	fmt.Printf("payload message type: %+s\n", payload.Message.Type)

	// Handle other webhook types
	if payload.Message.Type == "tool-calls" {
		fmt.Printf("tool-calls")
		for _, toolCall := range payload.Message.ToolCalls {
			fmt.Printf("toolCall: %+v\n", toolCall.Function.Name)
			switch toolCall.Function.Name {
			case "createAssistant":
				fmt.Printf("createAssistant tool call")
				// Validate customer exists
				if payload.Message.Customer == nil || payload.Message.Customer.Number == nil {
					log.Printf("Error: Customer phone number missing in createAssistant call")
					break
				}

				// Validate assistant description exists
				if toolCall.Function.Arguments.Assistant == nil {
					log.Printf("Error: Assistant description missing in createAssistant call")
					break
				}

				// Cache the assistant description for the phone number
				phoneNumber := *payload.Message.Customer.Number
				assistantDesc := *toolCall.Function.Arguments.Assistant
				phoneToAssistantCache[phoneNumber] = assistantDesc
				log.Printf("Successfully cached assistant description for phone number: %s", phoneNumber)

				// Return response in required format
				return c.JSON(fiber.Map{
					"results": []fiber.Map{
						{
							"toolCallId": toolCall.ID,
							"result":     "Assistant created successfully",
						},
					},
				})
			}
		}
	}

	return c.SendStatus(200)
}

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		log.Printf("Warning: Error loading .env file: %v", err)
	}

	// Initialize OpenAI client
	oai = openai.NewClient(os.Getenv("OPENAI_KEY"))

	app := fiber.New()

	// VAPI webhook endpoint
	app.Post("/api/v1/vapi/webhook", handleVAPIWebhook)

	log.Fatal(app.Listen(":3003"))
}
