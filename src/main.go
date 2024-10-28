package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/client"
	"github.com/joho/godotenv"
)

var (
	imageToWatch    string
	cooldownPeriod  = 60 * time.Second           // Zeitspanne von 60 Sekunden für das Timeout
	composeFilePath string                       // Pfad zur docker-compose.yml-Datei
	lastProcessed   = make(map[string]time.Time) // Letzte Bearbeitungszeit der Container speichern
)

func main() {
	// Lade die .env-Datei und weise die Werte zu
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, proceeding with system environment variables")
	}

	composeFilePath = getEnv("COMPOSE_FILE_PATH", "docker-compose.yml")
	imageToWatch = getEnv("IMAGE_TO_WATCH", "ghcr.io/awbclan/awgores:latest")

	// Docker-Client initialisieren
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}

	// Docker-Login
	if err = dockerLogin(); err != nil {
		log.Fatalf("Error logging in to ghcr.io: %v", err)
	}
	log.Printf("Logged in to ghcr.io as %s\n", os.Getenv("GHCR_USERNAME"))

	ctx := context.Background()
	go watchDockerEvents(ctx, cli)

	select {} // Endlosschleife blockiert die Hauptschleife
}

// Hilfsfunktion, um Umgebungsvariable oder Fallback zu verwenden
func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func dockerLogin() error {
	username := os.Getenv("GHCR_USERNAME")
	password := os.Getenv("GHCR_PASSWORD")

	if username == "" || password == "" {
		return fmt.Errorf("GHCR_USERNAME or GHCR_PASSWORD is not set")
	}

	cmd := exec.Command("docker", "login", "ghcr.io", "-u", username, "--password-stdin")
	cmd.Stdin = strings.NewReader(password)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker login failed: %v", err)
	}
	return nil
}

func watchDockerEvents(ctx context.Context, cli *client.Client) {
	messages, errs := cli.Events(ctx, types.EventsOptions{})
	for {
		select {
		case event := <-messages:
			handleDockerEvent(ctx, cli, event)
		case err := <-errs:
			log.Printf("Error while watching events: %v\n", err)
		}
	}
}

func handleDockerEvent(ctx context.Context, cli *client.Client, event events.Message) {
	if event.Type == events.ContainerEventType && (event.Action == "die" || event.Action == "restart") {
		container, err := cli.ContainerInspect(ctx, event.Actor.ID)
		if err != nil {
			if client.IsErrNotFound(err) {
				return
			}
			log.Printf("Error inspecting container: %v\n", err)
			return
		}

		if strings.Contains(container.Config.Image, imageToWatch) {
			containerName := strings.TrimPrefix(container.Name, "/")

			lastTime, exists := lastProcessed[containerName]
			if exists && time.Since(lastTime) < cooldownPeriod {
				log.Printf("Skipping container %s (recently processed)\n", containerName)
				return
			}

			lastProcessed[containerName] = time.Now()
			log.Printf("Container %s with image %s stopped. Updating to latest image...\n", containerName, imageToWatch)
			restartContainerWithDocker(containerName)
		}
	}
}

func restartContainerWithDocker(containerName string) {
	if err := executeDockerComposeCommand("kill", containerName); err != nil {
		log.Printf("Failed to kill container %s: %v\n", containerName, err)
		return
	}

	if err := executeDockerComposeCommand("pull", containerName); err != nil {
		log.Printf("Failed to pull the latest image for %s: %v\n", containerName, err)
		return
	}

	if err := executeDockerComposeCommand("rm", "-f", containerName); err != nil {
		log.Printf("Failed to remove container %s: %v\n", containerName, err)
		return
	}

	if err := executeDockerComposeCommand("up", "-d", containerName); err != nil {
		log.Printf("Failed to start container %s: %v\n", containerName, err)
		return
	}

	log.Printf("Container %s has been updated and restarted successfully.\n", containerName)
}

func executeDockerComposeCommand(command string, args ...string) error {
	fullArgs := append([]string{"compose", "-f", composeFilePath, command}, args...)
	cmd := exec.Command("docker", fullArgs...)
	cmdOutput, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command 'docker compose %s' failed with output: %s, error: %v", command, string(cmdOutput), err)
	}
	log.Printf("Command 'docker compose %s' succeeded", command)
	return nil
}
