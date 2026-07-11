FROM node:22-alpine

RUN apk add --no-cache curl

WORKDIR /app

COPY konnect-credentials/package.json ./
RUN npm install --omit=dev

COPY konnect-credentials/*.mjs ./
COPY konnect-credentials/catalog.json ./

ENV PORT=8091
ENV DATA_DIR=/data

VOLUME ["/data"]
EXPOSE 8091

CMD ["node", "server.mjs"]
