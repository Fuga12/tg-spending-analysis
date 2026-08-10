FROM postgres:16
COPY --chown=postgres:postgres bot /app/bot
USER postgres
ENTRYPOINT ["/app/bot"]
