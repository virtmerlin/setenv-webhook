FROM alpine:latest

ADD set-env-webhook /set-env-webhook
ENTRYPOINT ["./set-env-webhook"]
