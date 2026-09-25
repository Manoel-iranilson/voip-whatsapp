# Guia de integração — WaCalls

Guia em português para quem vai **consumir** a API do WaCalls a partir de outro sistema
(backend de produto, CRM, n8n). Cobre do zero até o download da gravação.

A referência técnica completa, em inglês e do ponto de vista de quem opera o servidor, está em
[`API.md`](API.md).

Os exemplos usam duas variáveis:

```bash
export WACALLS_URL="https://seu-host.exemplo"
export WACALLS_KEY="o-segredo-depois-dos-dois-pontos-da-sua-api-key"
```

---

## Índice

1. [O que esta API faz — e o que ela não faz](#1-o-que-esta-api-faz--e-o-que-ela-não-faz)
2. [Autenticação](#2-autenticação)
3. [Sessões: conectar uma conta do WhatsApp](#3-sessões-conectar-uma-conta-do-whatsapp)
4. [Disparar uma ligação](#4-disparar-uma-ligação)
5. [Acompanhar a ligação](#5-acompanhar-a-ligação)
6. [Histórico](#6-histórico)
7. [Gravações](#7-gravações)
8. [Receita completa](#8-receita-completa)
9. [Referência rápida](#9-referência-rápida)
10. [Checklist de produção](#10-checklist-de-produção)

---

## 1. O que esta API faz — e o que ela não faz

**Faz:** discar, atender, rejeitar, encerrar, limitar duração, acompanhar o ciclo de vida em
tempo real, consultar histórico e baixar gravações. Tudo isso é 100% HTTP, sem interface.

**Não faz: a API nunca transporta áudio.** Não existe forma de tocar um arquivo, uma URA ou um
TTS dentro da ligação. O único caminho de entrada de som é o microfone de um navegador
conectado ao painel, que faz o handshake WebRTC e assume a chamada.

Na prática isso significa:

| Cenário | Funciona? |
|---|---|
| Backend dispara a ligação, operador humano atende no painel | Sim |
| Backend dispara, ninguém atende no painel | A ligação conecta e fica **muda** — o servidor envia só silêncio |
| Bot de voz, URA, atendimento automático | Não, sem desenvolvimento novo no servidor |

Uma ligação sem operador ainda gera gravação, mas só com o áudio do interlocutor (ver
[Gravações](#7-gravações)).

---

## 2. Autenticação

O servidor aceita o mesmo segredo em **quatro lugares diferentes**. Use o que for mais
conveniente:

| Forma | Quando usar |
|---|---|
| `X-API-Key: <segredo>` | padrão para backend |
| `X-Auth-Token: <segredo>` | equivalente |
| `Authorization: Bearer <segredo>` | equivalente; é o que funciona em chamadas cross-origin do navegador |
| `?token=<segredo>` na URL | necessário no SSE, porque `EventSource` não envia headers |

**O que define o seu papel é o valor do segredo, não o header.** O servidor compara primeiro
contra as chaves de integração (`WACALLS_API_KEY`) e depois contra o token de operador
(`WACALLS_TOKEN`).

### Integração × operador

| | Operador (`WACALLS_TOKEN`) | Integração (`WACALLS_API_KEY`) |
|---|---|---|
| Quem é | pessoa no painel, no navegador | outro sistema chamando a API |
| Dono da chamada criada | ele mesmo | ninguém, para um operador reivindicar depois |
| Limite de 1 chamada por vez | sim | **não** — pode abrir várias, até `WACALLS_MAX_CALLS_PER_SESSION` |
| Recebe o áudio | sim | nunca |

A integração pode direcionar a ligação a um operador específico enviando `"operator": "<clientId>"`
no corpo do POST.

### Formato da chave

`WACALLS_API_KEY` usa o formato `nome:segredo`, com várias separadas por vírgula:

```
WACALLS_API_KEY=produto:SEGREDO_UM,n8n:SEGREDO_DOIS
```

O `nome` é só um rótulo para identificar quem chamou nos logs. **O que vai no header é apenas a
parte depois dos dois-pontos.** Um valor sem `:` faz o servidor abortar no startup com
`bad -api-key`.

### Erros de autenticação

| Status | Body |
|---|---|
| 401 | `{"error":"invalid or missing credential"}` |
| 403 | `{"error":"origin not allowed"}` — só ocorre em chamadas de navegador; requisição sem header `Origin` (curl, backend) nunca é barrada |

---

## 3. Sessões: conectar uma conta do WhatsApp

Uma **sessão** é uma conta do WhatsApp pareada. Cada sessão tem seu próprio QR, estado e
histórico. Várias sessões rodam lado a lado.

### Criar

```bash
curl -X POST "$WACALLS_URL/api/sessions" \
  -H "X-API-Key: $WACALLS_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"Comercial"}'
```

```json
{ "id": "8bd4016abd8bab6604795b79d8c2059d" }
```

Esse `id` é o `{sid}` de todas as outras rotas. A criação já dispara o pareamento.

> **O QR code não volta no corpo HTTP.** Ele chega apenas pelo SSE, nos eventos `session-qr` e
> `auth-state`. Se o seu fluxo for de backend, o caminho prático é criar a sessão pela API e
> parear uma única vez pelo painel no navegador.

### Listar

```bash
curl "$WACALLS_URL/api/sessions" -H "X-API-Key: $WACALLS_KEY"
```

```json
{
  "sessions": [
    { "id": "8bd4016...", "name": "Comercial", "jid": "5511999999999:12@s.whatsapp.net",
      "state": "connected", "paired": true }
  ]
}
```

Só dá para ligar quando `paired` é `true`.

### Novo QR, desconectar e apagar

| Método | Caminho | Efeito |
|---|---|---|
| POST | `/api/sessions/{sid}/pair` | gera novo QR (pelo SSE). `400 session already paired` se já estiver pareada |
| POST | `/api/sessions/{sid}/logout` | desconecta a conta |
| DELETE | `/api/sessions/{sid}` | desconecta e **apaga** a sessão. `204` |

> Apagar a sessão **não apaga as gravações em disco** nem as linhas do histórico. Veja
> [Retenção](#retenção-não-existe).

---

## 4. Disparar uma ligação

```bash
curl -X POST "$WACALLS_URL/api/sessions/$SID/calls" \
  -H "X-API-Key: $WACALLS_KEY" \
  -H "Content-Type: application/json" \
  -d '{"phone":"5511999999999","record":true,"duration_ms":300000}'
```

```json
{ "call": { "callId": "3DA59B0737FC3800E0526E7794C3BA52" } }
```

### Campos do corpo

| Campo | Tipo | Obrigatório | Descrição |
|---|---|---|---|
| `phone` | string | **sim** | número com DDI. Tudo que não for dígito é removido |
| `record` | bool | não | grava esta chamada. Ver [Gravações](#7-gravações) |
| `duration_ms` | int | não | encerra a chamada automaticamente após esse tempo. É a **única** trava automática de duração |
| `operator` | string | não | direciona a chamada a um operador específico (o `clientId` dele) |

### Erros

| Status | Body | Causa |
|---|---|---|
| 400 | `{"error":"phone required"}` | `phone` vazio ou JSON inválido |
| 400 | `{"error":"recording requested but the server was started without -record-dir"}` | pediu `record: true` com gravação desabilitada |
| 404 | `{"error":"no such session"}` | `sid` inexistente |
| 409 | `{"error":"operator already on a call"}` | só acontece com credencial de **operador** |
| 429 | `{"error":"max concurrent calls"}` | atingiu `WACALLS_MAX_CALLS_PER_SESSION` |
| 503 | `{"error":"not paired"}` | conta não conectada |

### Controlar a chamada

| Método | Caminho | Observação |
|---|---|---|
| POST | `/api/sessions/{sid}/calls/{id}/accept` | atende uma chamada recebida |
| POST | `/api/sessions/{sid}/calls/{id}/reject` | recusa |
| DELETE | `/api/sessions/{sid}/calls/{id}` | encerra. `204` |

Essas três rotas usam o header `X-Client-Id` para saber quem está controlando. Se a chamada já
tiver dono e o `X-Client-Id` for outro, a resposta é `409` — com mensagem
`claimed by another client` no `accept` e `call owned by another client` no `reject` e no
`DELETE`.

### Quem carrega o áudio

Uma chamada disparada pela API nasce **sem dono** e fica muda até que algum navegador assuma o
áudio. Reivindicar não é um endpoint separado: **anexar o áudio é o ato de reivindicar**, feito
pelo `POST /api/sessions/{sid}/calls/{id}/webrtc`. Duas formas:

| Caminho | O que o operador vê | Desenvolvimento |
|---|---|---|
| **Painel do WaCalls** | deixa uma aba aberta; a chamada aparece numa lista e ele clica em **Pick up** | nenhum |
| **SDK no seu front-end** ([`sdk/`](sdk/README.md)) | fala pela interface do seu produto; o painel deixa de existir para ele | importar o SDK e liberar `WACALLS_ALLOW_ORIGIN` |

O SDK não exige mudança no servidor — só a liberação de CORS para a origem do seu produto.

Em ambos os casos o `X-Client-Id` identifica o operador. Ele **precisa ser estável entre
reloads**: se mudar, o operador perde a chamada em que está. Para direcionar uma ligação a um
operador específico, mande `"operator": "<clientId>"` no POST — mas note que não existe endpoint
que liste os operadores conectados, então o seu sistema precisa conhecer esse id por conta
própria (com o SDK, você o define: `clientId: "corretor-42"`).

---

## 5. Acompanhar a ligação

Duas formas: **webhook** (recomendada para backend) e **SSE** (para telas em tempo real).

### Webhook

Configure no servidor:

```
WACALLS_WEBHOOK_URL=https://seu-backend/hooks/wacalls
WACALLS_WEBHOOK_SECRET=um-segredo-longo
```

O servidor faz `POST` em JSON a cada mudança de estado.

**Eventos:** `call.incoming`, `call.ringing`, `call.active`, `call.ended`.

**Payload:**

```json
{
  "type": "call.ended",
  "delivery": "9f2c1a7b3d4e5f60718293a4b5c6d7e8",
  "sentAt": 1790003812345,
  "sessionId": "8bd4016abd8bab6604795b79d8c2059d",
  "callId": "3DA59B0737FC3800E0526E7794C3BA52",
  "direction": "outbound",
  "peer": "5511999999999@s.whatsapp.net",
  "status": "ended",
  "owner": null,
  "startedAt": 1790003790260,
  "endedAt": 1790003812100,
  "endReason": "user_ended",
  "recording": "1790003790260-3DA59B0737FC3800E0526E7794C3BA52"
}
```

`owner` vem sempre, podendo ser `null`. `endedAt`, `endReason` e `recording` são omitidos
quando não se aplicam.

**Headers:**

| Header | Conteúdo |
|---|---|
| `X-WaCalls-Event` | o `type` do evento |
| `X-WaCalls-Delivery` | id único da entrega, útil para idempotência |
| `X-WaCalls-Signature` | `sha256=<hex>`, só quando há segredo configurado |

**Verificação da assinatura** — HMAC-SHA256 sobre o **corpo bruto**, antes de qualquer parse:

```js
import crypto from "node:crypto";

function valido(corpoBruto, header, segredo) {
  const esperado = "sha256=" + crypto.createHmac("sha256", segredo)
    .update(corpoBruto).digest("hex");
  const a = Buffer.from(esperado), b = Buffer.from(header || "");
  return a.length === b.length && crypto.timingSafeEqual(a, b);
}
```

```python
import hmac, hashlib

def valido(corpo_bruto: bytes, header: str, segredo: str) -> bool:
    esperado = "sha256=" + hmac.new(segredo.encode(), corpo_bruto, hashlib.sha256).hexdigest()
    return hmac.compare_digest(esperado, header or "")
```

> **Webhook é notificação, não registro contábil.** A fila interna tem 256 posições e **eventos
> são descartados se ela encher**. São 3 tentativas com backoff; um `4xx` que não seja `429`
> não é retentado. Para estado autoritativo, consulte o [histórico](#6-histórico).
>
> Há **uma única URL de webhook para o servidor inteiro**, não uma por sessão.

### SSE

```
GET /api/events?token=<segredo>&clientId=<seu-id>
```

Ao conectar você recebe um snapshot completo (`session-list` + `call-list`) e depois os eventos
individuais. Keepalive `: ping` a cada 20 s.

| Evento | Conteúdo |
|---|---|
| `session-list` | lista de sessões |
| `session-qr` | `{sessionId, qr}` — **é aqui que o QR chega** |
| `auth-state` | `{sessionId, paired, state, qr}` |
| `call-list` | lista de `CallRecord` completos (**inclui `recording`**) |
| `call-status` | `{sessionId, id, owner, status, peer, startedAt}` |
| `call-ended` | `{sessionId, id, owner, reason, endedAt}` |
| `incoming` | `{sessionId, id, peer, offeredAt}` |
| `incoming-claimed` | `{sessionId, id, owner}` |

Note que `call-status` e `call-ended` **não** trazem o campo `recording` — só `call-list` traz.

---

## 6. Histórico

```bash
curl "$WACALLS_URL/api/sessions/$SID/history" -H "X-API-Key: $WACALLS_KEY"
```

```json
{
  "rows": [
    {
      "sessionId": "8bd4016abd8bab6604795b79d8c2059d",
      "callId": "3DA59B0737FC3800E0526E7794C3BA52",
      "owner": null,
      "direction": "outbound",
      "peer": "5511999999999@s.whatsapp.net",
      "startedAt": 1790003790260,
      "status": "ended",
      "endedAt": 1790003812100,
      "endReason": "user_ended",
      "recording": "1790003790260-3DA59B0737FC3800E0526E7794C3BA52"
    }
  ]
}
```

| Campo | Tipo | Observação |
|---|---|---|
| `sessionId` | string | |
| `callId` | string | é o que entra na URL de download da gravação |
| `owner` | string ou `null` | sempre presente; `null` = ninguém assumiu |
| `direction` | string | `"inbound"` ou `"outbound"` |
| `peer` | string | JID completo |
| `startedAt` | int | Unix em **milissegundos** |
| `status` | string | `starting`, `ringing`, `connected`, `ended` |
| `endedAt` | int | Unix em ms; omitido se ausente |
| `endReason` | string | omitido se vazio |
| `recording` | string | **omitido quando não há gravação** |

> **Limite fixo de 50 linhas**, mais recentes primeiro. Não há paginação nem filtro por data:
> se o seu produto precisa de histórico longo, copie as linhas para o seu próprio banco
> conforme os webhooks chegam.

---

## 7. Gravações

### 7.1 Ligar a gravação

Consulte o modo atual:

```bash
curl "$WACALLS_URL/api/config" -H "X-API-Key: $WACALLS_KEY"
```

```json
{ "recording": "optional" }
```

| Valor | Significado |
|---|---|
| `"off"` | gravação desabilitada. `record: true` devolve **400** e o download devolve 404 |
| `"optional"` | grava **só** as chamadas com `record: true`. Chamadas recebidas **nunca** são gravadas |
| `"always"` | grava tudo, inclusive as recebidas. O campo `record` passa a ser irrelevante |

A imagem Docker já vem em `"optional"` (o Dockerfile define `WACALLS_RECORD_DIR=/data/recordings`).
Para `"always"`, defina `WACALLS_RECORD_ALL=true`.

> **Chamadas recebidas só são gravadas no modo `always`.** Não existe parâmetro para ligar a
> gravação de uma chamada inbound individual — o `accept` não aceita essa opção.

### 7.2 Descobrir que existe gravação

A regra é uma só:

> **Existe gravação quando a chave `recording` está presente e não é vazia.**

O campo é uma **string**, não um booleano e não `null`. Quando não há gravação, a chave
simplesmente **não aparece** no JSON. O valor é o *nome-base* do arquivo (sem extensão e sem
caminho) — o caminho em disco nunca é exposto pela API.

Onde o campo aparece:

| Origem | Traz `recording`? |
|---|---|
| `GET /api/sessions/{sid}/history` | sim |
| Webhook `call.ended` | sim |
| Webhook `call.ringing` / `call.active` | sim, mas o arquivo ainda não é baixável |
| Webhook `call.incoming` | não |
| SSE `call-list` | sim |
| SSE `call-status` / `call-ended` | não |

### 7.3 Quando baixar

> **Não é possível baixar a gravação durante a chamada.** A tentativa devolve
> `404 no recording for this call`.

Dois motivos: a linha do histórico só é escrita no encerramento, e o cabeçalho do arquivo WAV
(que carrega o tamanho real) só é corrigido quando o arquivo é fechado.

**O gatilho correto é o webhook `call.ended`** — é o único evento emitido depois que o arquivo
foi fechado *e* o histórico foi gravado. Quando ele chega, o arquivo está íntegro e pronto.

### 7.4 Baixar

```bash
curl -L -o gravacao.wav \
  -H "X-API-Key: $WACALLS_KEY" \
  "$WACALLS_URL/api/sessions/$SID/calls/$CALL_ID/recording"
```

Resposta `200`:

```
Content-Type: audio/wav
Content-Disposition: attachment; filename="1790003790260-3DA59B07....wav"
Accept-Ranges: bytes
```

Suporta `Range`, então dá para retomar downloads interrompidos.

Para tocar direto num `<audio>` do navegador, use a credencial na query string:

```html
<audio controls src="https://seu-host/api/sessions/SID/calls/CALL_ID/recording?token=SEGREDO"></audio>
```

**Erros — todos são 404, distinguidos pela mensagem:**

| Body | Significado |
|---|---|
| `{"error":"no such session"}` | `sid` não existe |
| `{"error":"recording is not enabled"}` | servidor sem diretório de gravação configurado |
| `{"error":"no recording for this call"}` | a chamada não foi gravada, ou ainda está em andamento |
| `{"error":"recording file is gone"}` | o histórico diz que existe, mas o arquivo sumiu do disco |

### 7.5 Formato do arquivo

| Propriedade | Valor |
|---|---|
| Container | WAV PCM (não comprimido) |
| Taxa de amostragem | 16.000 Hz |
| Profundidade | 16 bits, little-endian |
| Canais | 2 (estéreo) |
| **Canal esquerdo** | **operador** (áudio vindo do navegador) |
| **Canal direito** | **interlocutor** (a outra ponta da ligação) |

Tamanho: **~3,8 MB por minuto**, cerca de **230 MB por hora** de ligação.

Separar as duas vozes é trivial com ffmpeg:

```bash
ffmpeg -i gravacao.wav -filter_complex "channelsplit=channel_layout=stereo[esq][dir]" \
  -map "[esq]" operador.wav -map "[dir]" interlocutor.wav
```

### 7.6 Dois avisos que a API não sinaliza

**1. `200` no POST não garante gravação.** Se o gravador falhar ao abrir o arquivo (disco
cheio, permissão errada no volume), a chamada prossegue normalmente **sem gravar** e isso
aparece só no log do servidor. O mesmo vale se o disco encher no meio: o arquivo fica truncado,
mas o histórico continua apontando para ele e o download devolve 200.

**2. Chamada sem operador gera gravação pela metade.** O áudio do interlocutor é gravado
independentemente de haver navegador conectado, mas o áudio do operador só existe se alguém
assumiu a chamada no painel. O resultado é um WAV com o **canal esquerdo em silêncio**.

### 7.7 Retenção: não existe

**Nada é apagado automaticamente.** Não há rotina de limpeza, TTL, limite de tamanho ou cota.

- As linhas de `call_history` nunca são removidas — o `GET /history` só mostra as 50 mais
  recentes, mas as antigas continuam no banco.
- Apagar uma sessão **não apaga** os arquivos `.wav` nem as linhas do histórico. As gravações
  ficam órfãs: inacessíveis pela API (a sessão não existe mais) e ainda ocupando disco.

São gravações de voz de pessoas reais. Defina uma política de retenção no seu produto: copie o
arquivo para o seu storage, apague do servidor, e decida por quanto tempo guarda.

---

## 8. Receita completa

```bash
export WACALLS_URL="https://seu-host.exemplo"
export WACALLS_KEY="seu-segredo"

# 1. Confirmar que a gravação está habilitada
curl -s "$WACALLS_URL/api/config" -H "X-API-Key: $WACALLS_KEY"
# {"recording":"optional"}

# 2. Descobrir a sessão pareada
curl -s "$WACALLS_URL/api/sessions" -H "X-API-Key: $WACALLS_KEY"
export SID="8bd4016abd8bab6604795b79d8c2059d"

# 3. Disparar a ligação, pedindo gravação e limitando a 5 minutos
curl -s -X POST "$WACALLS_URL/api/sessions/$SID/calls" \
  -H "X-API-Key: $WACALLS_KEY" -H "Content-Type: application/json" \
  -d '{"phone":"5511999999999","record":true,"duration_ms":300000}'
# {"call":{"callId":"3DA59B07..."}}
export CALL_ID="3DA59B07..."

# 4. Um operador assume a chamada no painel, no navegador.
#    Sem isso não há áudio — e a gravação sai com o canal esquerdo mudo.

# 5. Ao receber o webhook call.ended (ou consultando o histórico),
#    verificar se o campo "recording" veio preenchido
curl -s "$WACALLS_URL/api/sessions/$SID/history" -H "X-API-Key: $WACALLS_KEY"

# 6. Baixar
curl -s -o "$CALL_ID.wav" -H "X-API-Key: $WACALLS_KEY" \
  "$WACALLS_URL/api/sessions/$SID/calls/$CALL_ID/recording"

# 7. Guardar no seu storage e apagar do servidor por conta própria.
```

---

## 9. Referência rápida

### Endpoints

| Método | Caminho | Para quê |
|---|---|---|
| GET | `/healthz` | saúde do serviço. **Não exige credencial** |
| GET | `/api/config` | modo de gravação |
| GET | `/api/sessions` | listar contas |
| POST | `/api/sessions` | criar conta |
| POST | `/api/sessions/{sid}/pair` | novo QR (chega pelo SSE) |
| POST | `/api/sessions/{sid}/logout` | desconectar |
| DELETE | `/api/sessions/{sid}` | apagar |
| POST | `/api/sessions/{sid}/calls` | disparar ligação |
| POST | `/api/sessions/{sid}/calls/{id}/accept` | atender |
| POST | `/api/sessions/{sid}/calls/{id}/reject` | recusar |
| DELETE | `/api/sessions/{sid}/calls/{id}` | encerrar |
| GET | `/api/sessions/{sid}/history` | histórico (50 linhas) |
| GET | `/api/sessions/{sid}/calls/{id}/recording` | baixar gravação (WAV) |
| GET | `/api/events` | SSE |

### Variáveis de ambiente

| Variável | Para quê |
|---|---|
| `WACALLS_TOKEN` | credencial do navegador. **Obrigatória** se houver API key, senão o painel é rejeitado |
| `WACALLS_API_KEY` | credencial de integração, formato `nome:segredo` |
| `WACALLS_PUBLIC_IP` | IP público da VPS. Obrigatório atrás de NAT, e tem que ser um IP puro |
| `WACALLS_UDP_PORT_MIN` / `_MAX` | faixa de portas de mídia; precisa bater com as portas publicadas no host |
| `WACALLS_RECORD_DIR` | diretório das gravações. Vazio = gravação desligada |
| `WACALLS_RECORD_ALL` | grava tudo, inclusive chamadas recebidas |
| `WACALLS_WEBHOOK_URL` / `_SECRET` | destino e assinatura dos webhooks |
| `WACALLS_MAX_CALLS_PER_SESSION` | chamadas simultâneas por conta (padrão 8) |
| `WACALLS_ALLOW_ORIGIN` | só necessário se um front-end de outro domínio chamar a API |
| `WACALLS_DEBUG` | log verboso. Use apenas para diagnóstico |

---

## 10. Checklist de produção

- [ ] Volume montado em `/data` — é onde ficam as credenciais do WhatsApp e as gravações. Sem
      ele, todo deploy apaga tudo e exige parear de novo por QR.
- [ ] `WACALLS_TOKEN` e `WACALLS_API_KEY` com segredos longos e aleatórios
      (`openssl rand -base64 24`).
- [ ] Uma única réplica do serviço. O banco é SQLite com uma conexão só; várias réplicas
      corrompem o estado e disputam as portas de mídia.
- [ ] Faixa de portas UDP publicada no host e batendo com `WACALLS_UDP_PORT_MIN/MAX`.
- [ ] HTTPS com certificado válido. Sem isso o navegador não libera o microfone e não há áudio.
- [ ] Política de retenção das gravações definida e implementada no seu lado.
- [ ] Webhook tratado como best-effort; histórico como fonte de verdade.
