APP_NAME := nikodis
BUILD_DIR := ./bin
INSTALL_DIR := /opt/$(APP_NAME)
SERVICE_FILE := $(APP_NAME).service

.PHONY: proto build setup install start stop restart logs status prod

# --- Build ---

proto:
	@mkdir -p pkg/gen
	protoc \
		--proto_path=proto \
		--go_out=pkg/gen --go_opt=paths=source_relative \
		--go-grpc_out=pkg/gen --go-grpc_opt=paths=source_relative \
		nikodis.proto

build:
	go build -o $(BUILD_DIR)/$(APP_NAME) ./cmd/nikodis/

test:
	go test ./... -count=1 -race

# --- Deployment ---

setup: build
	@echo "Creating system user..."
	sudo useradd --system --no-create-home --shell /usr/sbin/nologin $(APP_NAME) 2>/dev/null || true
	@echo "Creating installation directory..."
	sudo mkdir -p $(INSTALL_DIR)
	sudo cp $(BUILD_DIR)/$(APP_NAME) $(INSTALL_DIR)/$(APP_NAME)
	sudo chown -R $(APP_NAME):$(APP_NAME) $(INSTALL_DIR)
	@echo "Installing systemd service..."
	sudo cp $(SERVICE_FILE) /etc/systemd/system/$(SERVICE_FILE)
	sudo systemctl daemon-reload
	sudo systemctl enable $(APP_NAME)
	@echo ""
	@echo "Setup complete. Next steps:"
	@echo "  1. Create $(INSTALL_DIR)/.env (see .env.example)"
	@echo "  2. Create $(INSTALL_DIR)/config.json for namespace auth (see config.example.json)"
	@echo "  3. Run: sudo systemctl start $(APP_NAME)"

install: build
	@echo "Deploying new binary..."
	sudo systemctl stop $(APP_NAME) || true
	sudo cp $(BUILD_DIR)/$(APP_NAME) $(INSTALL_DIR)/$(APP_NAME)
	sudo chown $(APP_NAME):$(APP_NAME) $(INSTALL_DIR)/$(APP_NAME)
	sudo systemctl start $(APP_NAME)
	@echo "Deployed and restarted."

prod:
	git pull
	$(MAKE) install

# --- Service management ---

start:
	sudo systemctl start $(APP_NAME)

stop:
	sudo systemctl stop $(APP_NAME)

restart:
	sudo systemctl restart $(APP_NAME)

logs:
	sudo journalctl -u $(APP_NAME) -f

status:
	sudo systemctl status $(APP_NAME)
