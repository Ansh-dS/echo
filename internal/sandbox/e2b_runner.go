package sandbox

import (
	"context"
	"errors"
	"os"

	"github.com/eric642/e2b-go-sdk"
)

type E2BClient struct {
	client *e2b.Client
}

func NewE2BClient() (*E2BClient, error) {
	apiKey := os.Getenv("E2B_API_KEY")
	if apiKey == "" {
		return nil, errors.New("E2B_API_KEY environment variable is not set")
	}

	client, err := e2b.NewClient(e2b.Config{APIKey: apiKey})
	if err != nil {
		return nil, err
	}

	return &E2BClient{client: client}, nil
}

func (c *E2BClient) CreateSandbox(ctx context.Context) (*e2b.Sandbox, error) {
	
}

func (c *E2BClient) CommandsToSandbox(ctx context.Context, sandbox *e2b.Sandbox, command string) (string, string, error) {
	return "", "", nil
}

func (c *E2BClient) CloseSandbox(ctx context.Context, sandbox *e2b.Sandbox) error {
	return nil
}


func main(){
	e2bClient, err := NewE2BClient()
	if(err!= nil ) 

}