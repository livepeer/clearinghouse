FROM node:22-alpine

RUN apk add --no-cache curl

WORKDIR /app

COPY identity-webhook/package.json identity-webhook/package-lock.json ./
RUN npm ci --omit=dev

COPY identity-webhook/keys.mjs identity-webhook/protocol.mjs identity-webhook/verifiers.mjs identity-webhook/server.mjs ./

ENV PORT=8090

EXPOSE 8090

CMD ["node", "server.mjs"]
