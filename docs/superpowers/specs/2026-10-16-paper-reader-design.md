# Paper Reader — 설계 문서

- 작성일: 2026-10-16
- 상태: 설계 승인됨, 구현 계획 작성 전

## 1. 목적

개인용 논문 리더. PDF 논문을 **한 페이지씩 딥다이브**하며 읽는 웹 앱이다.
요약 도구가 아니라 **번역이 주 목적**이고, 페이지별 해설과 선택 영역 Q&A를 곁들인다.

- 사용자: 본인 1명
- LLM: Anthropic API가 아니라 **`claude -p` CLI + Claude 구독**을 사용한다
- 번역 방향: 영어 → 한국어

### 성공 기준

- PDF를 올리면 페이지를 열 때 해당 페이지의 블록별 한국어 번역과 해설이 스트리밍으로 생성된다
- 한 번 생성된 결과는 DB에 저장되어, 다시 열면 즉시 표시된다
- 논문 전체에서 용어 번역이 일관된다 (용어집)
- 원문 블록이나 번역 텍스트를 선택해 질문하고, 이어서 대화할 수 있다

## 2. 배포 환경

| 단계 | 앱 실행 위치 | DB | 파일 저장소 |
|---|---|---|---|
| 개발 | Mac 로컬 | TrueNAS Postgres (Tailscale 경유) 또는 로컬 docker Postgres | 로컬 `DATA_DIR` |
| 운영 | TrueNAS Apps (docker 컨테이너) | TrueNAS Postgres (Tailscale) | TrueNAS 데이터셋을 컨테이너에 마운트 |

- 접근은 Tailscale 네트워크 내부로 한정한다. 앱 자체 인증은 없다.
- 컨테이너 안의 `claude` 인증: Mac에서 `claude setup-token`으로 발급한 장기 토큰을 환경변수 `CLAUDE_CODE_OAUTH_TOKEN`으로 주입한다.
- 최종 산출물: React 빌드 결과를 `embed`한 **Go 단일 바이너리** + `claude` CLI, `mutool`(mupdf-tools), `infisical` CLI를 포함한 docker 이미지. 이미지 빌드 시 `mutool draw -F stext.json` 지원 여부를 검사한다.

### 구현 계획 분할

1. 백엔드 (API를 curl로 끝까지 검증 가능)
2. 프론트엔드 (React, Go 바이너리에 embed)
3. 배포 (Dockerfile, TrueNAS Apps, Infisical prod)

## 3. 기술 스택

- 백엔드: Go
  - HTTP: 표준 `net/http` (Go 1.22+ 라우팅 패턴)
  - PDF: MuPDF CLI `mutool` (서브프로세스). `mutool draw -r <DPI>`로 PNG 렌더링, `mutool draw -F stext.json`으로 블록/줄 + bbox 추출. cgo 없음. (go-fitz는 블록 단위 bbox를 제공하지 않아 제외 — 2026-10-17 검증)
  - DB: Postgres + `github.com/jackc/pgx/v5`, 마이그레이션은 `github.com/pressly/goose/v3`
  - LLM: `claude -p` 서브프로세스 (`os/exec`)
  - 실시간 전달: SSE
- 프론트엔드: React + Vite + TypeScript
  - 마크다운/수식: `react-markdown` + `remark-math` + `rehype-katex`
  - 라우팅: `react-router`

## 4. 프로젝트 구조

```
paper-reader/
├── backend/
│   ├── cmd/server/main.go        # 설정 로드 → 의존성 조립 → 서버 시작
│   ├── internal/
│   │   ├── config/               # 환경변수 → Config
│   │   ├── domain/               # Paper, Page, Block, PageResult, GlossaryTerm, Thread, Message
│   │   ├── handler/              # HTTP (= controller). router.go, paper.go, page.go, ask.go, dto.go
│   │   ├── service/              # 비즈니스 로직. paper.go, page.go, ask.go
│   │   ├── store/                # Postgres (= repository). db.go, migrations/*.sql, *_repo.go
│   │   ├── pdf/                  # mutool 실행 + stext.json 파싱
│   │   ├── llm/                  # claude -p 실행기, stream-json 파서, prompts/*.tmpl (go:embed)
│   │   └── jobs/                 # 생성 작업 큐 / 워커
│   ├── go.mod
│   └── go.sum
├── web/                          # React + Vite
│   └── src/{pages,components,api}/
├── data/                         # 개발용 DATA_DIR (git 제외)
├── docs/
├── Makefile
└── Dockerfile
```

### 레이어 규칙

- 의존 방향: `handler → service → (store | pdf | llm | jobs)`. 역방향 import 금지.
- service는 자신이 필요로 하는 인터페이스를 **service 패키지 안에서** 정의한다. store/pdf/llm은 구현만 제공한다.
- 의존성 조립은 `cmd/server/main.go`에서 수동으로 한다 (DI 프레임워크 없음).
- handler는 요청 파싱, 응답 직렬화, 에러 → HTTP 상태 매핑만 한다.

## 5. 설정 (환경변수)

- 앱은 **환경변수만** 읽는다. 시크릿 저장소에 의존하는 코드나 `.env` 로더는 두지 않는다.
- 시크릿은 self-hosted **Infisical**(TrueNAS)의 `paper-reader` 프로젝트에서 관리한다. 환경: `dev`(Development), `prod`(Production).
- 개발: `make dev`가 `infisical run --env=dev -- ...`로 서버를 띄운다. 저장소 루트의 `.infisical.json`(프로젝트 ID만 들어 있음)은 커밋한다.
- 운영: 컨테이너 시작 명령이 `infisical run --env=prod -- ./server`다. 컨테이너에는 Machine Identity 자격증명(`INFISICAL_CLIENT_ID`, `INFISICAL_CLIENT_SECRET`)과 `INFISICAL_API_URL`만 TrueNAS 앱 환경변수로 직접 넣는다.
- 시크릿이 아닌 설정(`WORKERS` 등)은 기본값을 쓰고, 바꿀 때만 Infisical에 추가한다.

| 변수 | 기본값 | 설명 |
|---|---|---|
| `DATABASE_URL` | (필수) | Postgres 연결 문자열 |
| `DATA_DIR` | `./data` | PDF/PNG 저장 루트 |
| `LISTEN_ADDR` | `:8080` | HTTP 주소 |
| `CLAUDE_BIN` | `claude` | CLI 경로 |
| `CLAUDE_MODEL` | (CLI 기본값) | `--model`로 전달. 비우면 생략 |
| `WORKERS` | `2` | 동시 생성 워커 수 |
| `JOB_TIMEOUT` | `5m` | 작업당 타임아웃 |
| `RENDER_DPI` | `150` | 페이지 PNG 해상도 |
| `MUTOOL_BIN` | `mutool` | MuPDF CLI 경로 |
| `CLAUDE_CODE_OAUTH_TOKEN` | — | 운영 컨테이너에서 claude 인증 (앱은 읽지 않고 자식 프로세스에 그대로 상속) |

## 6. 파일 저장 레이아웃

```
$DATA_DIR/papers/<paper_id>/original.pdf
$DATA_DIR/papers/<paper_id>/pages/<page_no 3자리>.png   # 예: 001.png (1부터 시작)
```

DB에는 `DATA_DIR` 기준 상대 경로만 저장한다.

## 7. 데이터 모델 (Postgres)

```sql
papers (
  id          text primary key,         -- Go에서 생성한 UUID 문자열
  title       text not null,            -- 업로드 파일명에서 확장자를 뺀 값
  filename    text not null,
  page_count  int  not null,
  status      text not null,            -- 'processing' | 'ready' | 'failed'
  error       text,
  created_at  timestamptz not null default now()
)

pages (
  paper_id    text references papers(id) on delete cascade,
  page_no     int  not null,            -- 1부터
  width       real not null,            -- PDF 포인트 단위
  height      real not null,
  image_path  text not null,
  blocks      jsonb not null,           -- [{id, bbox:{x,y,w,h}, text}]  (PDF 포인트, 좌상단 원점)
  primary key (paper_id, page_no)
)

page_results (
  paper_id        text,
  page_no         int,
  status          text not null,        -- 'pending' | 'running' | 'done' | 'failed'
  translation     jsonb,                -- [{block_id, ko}]
  explanation     text,                 -- 마크다운 (KaTeX 수식 포함)
  page_summary    text,                 -- 한두 문장 요지. 다음 페이지 프롬프트 컨텍스트로 사용
  prompt_version  int  not null,
  error           text,
  updated_at      timestamptz not null default now(),
  primary key (paper_id, page_no),
  foreign key (paper_id, page_no) references pages on delete cascade
)

glossary (
  paper_id   text references papers(id) on delete cascade,
  term_en    text not null,
  term_ko    text not null,
  note       text,
  primary key (paper_id, term_en)
)

threads (
  id          text primary key,
  paper_id    text not null,
  page_no     int  not null,
  anchor      jsonb not null,           -- {block_ids:[...], quote:"선택한 텍스트"}
  created_at  timestamptz not null default now(),
  foreign key (paper_id, page_no) references pages on delete cascade
)

messages (
  id          bigserial primary key,
  thread_id   text not null references threads(id) on delete cascade,
  role        text not null,            -- 'user' | 'assistant'
  content     text not null,
  status      text not null,            -- 'done' | 'streaming' | 'failed'
  created_at  timestamptz not null default now()
)
```

- 블록 `id`는 페이지 내 순번 문자열(`b0`, `b1`, …)이다.
- 블록은 텍스트 블록만 저장한다 (stext.json의 `type:"text"`). 블록 텍스트는 줄을 공백으로 잇고, `-`로 끝나는 줄은 공백 없이 잇는다. 그림·수식 영역은 LLM이 페이지 이미지를 보고 해설에서 다룬다.
- 페이지 크기(pt) = PNG 픽셀 × 72 / `RENDER_DPI`.
- 용어집은 `(paper_id, term_en)` 기준으로 **먼저 들어온 번역이 이긴다** (`on conflict do nothing`). 수동 편집 UI는 범위 밖이다.

## 8. 주요 흐름

### 8.1 업로드

1. `POST /api/papers` (multipart) → `papers` 행을 `processing`으로 생성하고 PDF를 저장한 뒤 `201`을 즉시 반환한다.
2. 백그라운드 고루틴: mutool로 페이지별 PNG 렌더링과 텍스트 블록 추출 → `pages`에 일괄 삽입 → `ready`로 변경.
3. 실패하면 `failed`와 에러 메시지를 남긴다. 프론트는 목록을 폴링(2초)해서 상태를 표시한다.

LLM은 업로드 단계에서 호출하지 않는다.

### 8.2 페이지 열기 / 생성

1. `GET /api/papers/{id}/pages/{n}` → 이미지 URL, 블록, `page_results`(있으면)를 반환한다.
2. 결과가 `done`이면 바로 표시하고 끝난다.
3. 결과가 없거나 `failed`이면 프론트가 `POST /api/papers/{id}/pages/{n}/generate`를 호출한다.
   - service는 `page_results`를 `pending`으로 upsert하고 작업을 큐에 넣는다.
   - 이미 `pending`/`running`이면 아무것도 하지 않는다 (중복 방지).
   - `?force=1`이면 `done` 상태도 다시 생성한다 ([다시 생성] 버튼).
4. 프론트는 `GET /api/papers/{id}/pages/{n}/stream` (SSE)를 구독한다.
5. 워커:
   - `running`으로 변경
   - 프롬프트 구성: 페이지 이미지(base64) + 블록 목록 + 컨텍스트(논문 제목, 1페이지 블록 텍스트 앞부분 = 초록 근사, 직전 페이지 `page_summary`, 현재 용어집)
   - `claude -p` 실행 → 출력을 파싱하면서 블록 번역 단위로 SSE 이벤트 발행
   - 완료되면 `translation`/`explanation`/`page_summary`를 저장하고, 새 용어를 `glossary`에 upsert한 뒤 `done`으로 변경
6. 재시작 복구: 서버 시작 시 `pending`/`running` 결과를 `failed`("서버 재시작으로 중단됨")로 바꾼다.

### 8.3 사전 생성

`POST /api/papers/{id}/prefetch {from, count}` → 각 페이지에 대해 8.2의 3번과 같은 enqueue를 **낮은 우선순위**로 한다. 사용자가 직접 연 페이지가 항상 먼저 처리된다.

### 8.4 질문

1. `POST /api/papers/{id}/pages/{n}/threads {anchor, question}` → 스레드와 사용자 메시지를 만들고, 비어 있는 assistant 메시지(`streaming`)를 만든다.
2. `POST /api/threads/{tid}/messages {question}` → 기존 스레드에 이어서 질문한다.
3. 둘 다 응답 본문 자체를 SSE로 스트리밍한다 (`text/event-stream`). 작업 큐를 거치지 않고 요청 고루틴에서 바로 `claude -p`를 실행한다 (대화형이므로 즉시 처리).
4. 프롬프트: 페이지 이미지 + 블록 + 해당 페이지 번역(있으면) + anchor + 대화 기록 전체.
5. 완료되면 assistant 메시지 내용을 저장하고 `done`, 실패하면 `failed`.
6. `GET /api/papers/{id}/pages/{n}/threads` → 페이지의 스레드와 메시지 목록.

대화 상태의 원본은 DB다. `claude --resume` 세션에 의존하지 않는다.

## 9. LLM 연동 (`llm` 패키지)

### 9.1 호출 방식

```
claude -p --input-format stream-json --output-format stream-json --verbose \
       --include-partial-messages [--model $CLAUDE_MODEL] \
       --tools "" --system-prompt "<역할 프롬프트>"
```

- stdin으로 user 메시지 1개를 보낸다. content는 `[{type:"image", source:{type:"base64", media_type:"image/png", data}}, {type:"text", text:<프롬프트>}]`.
- 도구는 모두 비활성화한다 (번역/해설에 도구가 필요 없음, 속도와 안전).
- stdout의 stream-json에서 텍스트 델타를 모아 상위로 전달한다.
- 종료 코드가 0이 아니거나 `result` 이벤트가 에러이면 stderr/결과 메시지를 에러로 반환한다.

- 추가 플래그: `--no-session-persistence --strict-mcp-config --setting-sources ""`. 작업 디렉토리는 빈 임시 디렉토리로 둔다 (프로젝트 CLAUDE.md 등 로드 방지).
- **검증 완료 (2026-10-17, CLI 2.1.292):** base64 이미지 입력 동작, 텍스트 증분은 `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":...}}}`, 최종은 `{"type":"result","is_error":bool,"result":string}`.

### 9.2 인터페이스

```go
type Request struct {
    System string
    Image  []byte   // PNG, nil 가능
    Prompt string
}
type Runner interface {
    // onDelta는 텍스트 조각마다 호출된다. 반환값은 전체 텍스트.
    Run(ctx context.Context, req Request, onDelta func(string)) (string, error)
}
```

### 9.3 페이지 생성 출력 포맷

스트리밍 중에도 블록 단위로 끊어서 보여줄 수 있도록 JSON이 아니라 **마커 기반 텍스트**로 받는다.

```
@@BLOCK b0
3. 방법
@@BLOCK b1
우리는 트랜스포머 구조를 채택한다 ...
@@EXPLANATION
## 이 페이지 요지
...
@@SUMMARY
이 페이지는 ... 를 설명한다.
@@GLOSSARY
attention | 어텐션 | 주의 메커니즘으로 풀어쓰지 않음
@@END
```

- 파서는 줄 단위 상태 기계다. `@@BLOCK <id>`가 나올 때마다 직전 블록을 확정하고 SSE `block` 이벤트를 보낸다.
- 번역에서 생략 가능한 블록(쪽번호, 머리글 등)은 LLM이 `@@BLOCK <id>`를 내보내지 않아도 된다. 화면에는 원문만 표시한다.
- `@@END`가 없거나 섹션 구조가 깨지면 파싱 실패 → 1회 자동 재시도 → 그래도 실패하면 `failed`.
- 프롬프트 템플릿은 `llm/prompts/page.tmpl`, `ask.tmpl`에 두고, 바꿀 때 `prompt_version`을 올린다.

### 9.4 SSE 이벤트 (페이지 생성)

| event | data |
|---|---|
| `status` | `{"status":"running"}` |
| `block` | `{"block_id":"b1","ko":"..."}` |
| `explanation` | `{"delta":"..."}` (해설 섹션 텍스트 증분) |
| `done` | `{}` (프론트는 `GET` 결과를 다시 불러온다) |
| `error` | `{"message":"..."}` |

구독을 시작했을 때 이미 `done`이면 즉시 `done`을 보낸다. `running` 중에 구독하면 지금까지 확정된 블록을 먼저 보내고 이어서 실시간으로 보낸다. jobs 패키지가 작업별 진행 버퍼와 구독자 목록을 메모리에 가진다.

## 10. 작업 큐 (`jobs` 패키지)

- 메모리 큐 2단계 우선순위 (`high`: 사용자가 연 페이지, `low`: 사전 생성)
- 워커 `WORKERS`개. 키 `(paper_id, page_no)` 기준으로 중복 등록을 무시한다.
- low 대기 중인 작업이 high로 다시 요청되면 high로 승격한다.
- 작업마다 `JOB_TIMEOUT` 컨텍스트를 건다.
- 큐는 메모리에만 있다. 재시작 시 8.2의 6번 규칙으로 정리한다.

## 11. HTTP API 요약

| 메서드 | 경로 | 설명 |
|---|---|---|
| GET | `/api/papers` | 목록 (+ 페이지별 done 개수) |
| POST | `/api/papers` | 업로드 |
| GET | `/api/papers/{id}` | 상세 |
| DELETE | `/api/papers/{id}` | 삭제 (DB cascade + 파일 디렉토리 삭제) |
| GET | `/api/papers/{id}/pages/{n}` | 페이지 데이터 + 결과 |
| GET | `/api/papers/{id}/pages/{n}/image` | PNG |
| POST | `/api/papers/{id}/pages/{n}/generate` | 생성 요청 (`?force=1`) |
| GET | `/api/papers/{id}/pages/{n}/stream` | 생성 SSE |
| POST | `/api/papers/{id}/prefetch` | 사전 생성 `{from, count}` |
| GET | `/api/papers/{id}/pages/{n}/threads` | 스레드 목록 |
| POST | `/api/papers/{id}/pages/{n}/threads` | 새 질문 (SSE 응답) |
| POST | `/api/threads/{tid}/messages` | 이어서 질문 (SSE 응답) |
| GET | `/*` | React SPA (embed) |

에러 응답 형식: `{"error": "메시지"}` + 적절한 상태 코드 (400 / 404 / 409 / 500).

## 12. 화면

### Library (`/`)
- 논문 카드 목록: 제목, 쪽수, 번역 진행(`done`/전체), 상태(processing/failed 표시)
- 드래그 앤 드롭 업로드, 삭제(확인 단계 포함)

### Reader (`/papers/:id/:page`)
- **좌우 분할.** 왼쪽에 원문 PNG, 그 위에 블록 bbox 오버레이(hover/선택 하이라이트)
- 오른쪽 탭
  - **번역:** 블록 순서대로 번역. hover하면 왼쪽 bbox가 하이라이트되고, 반대 방향도 연동
  - **해설:** `explanation` 마크다운 + KaTeX
  - **질문:** 스레드 목록, 대화, 입력창
- 상태 표시: 생성 중(스트리밍), 실패(에러 메시지 + [재시도]), 완료([다시 생성])
- 툴바: ◀ ▶, 키보드 ←/→, 페이지 번호 입력, [다음 N장 미리 생성] (N 기본 3)
- 질문 진입: 원문 블록 클릭 또는 번역 텍스트 드래그 선택 → 💬 버튼 → 질문 탭에 새 스레드 작성창
- 좁은 화면(< 900px): 원문/번역/해설/질문을 하나의 탭 바로 전환

## 13. 에러 처리

| 상황 | 처리 |
|---|---|
| `claude` 비정상 종료, 사용량 한도 | `failed` + CLI 에러 메시지 그대로 표시. 자동 재시도 없음 |
| 출력 마커 파싱 실패 | 1회 자동 재시도 후 `failed` |
| 타임아웃 | 프로세스 kill, `failed`("시간 초과") |
| PDF 처리 실패 | `papers.status = failed` + 에러 |
| 서버 재시작 | 진행 중이던 작업을 `failed`로 변경 |
| 클라이언트 SSE 끊김 | 작업은 계속 진행. 다시 구독하면 버퍼를 재전송 |

## 14. 테스트

- `service`: 가짜 repo/llm/jobs로 단위 테스트 (캐시 히트, 중복 enqueue, force, 실패 전이, 용어집 반영)
- `llm`: 녹화한 stream-json 픽스처로 델타 추출 테스트. 마커 파서는 정상/조각난 입력/깨진 구조를 테이블 테스트로 검증
- `pdf`: `testdata/`의 작은 샘플 PDF로 페이지 수, PNG 생성, 블록 추출 검증
- `jobs`: 우선순위, 중복 제거, 승격, 구독자 재전송
- `store`: `TEST_DATABASE_URL`이 있을 때만 도는 Postgres 통합 테스트
- 프론트: 마커/SSE 처리 유틸만 vitest. 화면은 수동 확인

## 15. 범위 밖

- 앱 인증 (Tailscale에 위임)
- arXiv 소스(LaTeX/HTML) 연동
- 다중 사용자
- 전체 텍스트 검색, 내보내기
- 용어집 편집 UI
- 스캔본 OCR (이미지 입력으로 LLM이 읽는 것은 허용하지만, 블록 추출은 텍스트 레이어에 의존)
