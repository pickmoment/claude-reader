# Claude Reader

Claude Code 세션 파일을 읽고 시각화하는 웹 뷰어. `~/.claude/projects/` 의 JSONL 파일을 직접 파싱해 대화 내용, 토큰 사용량, 비용 추정치, 사용 패턴을 브라우저에서 보여준다.

## 기능

- **대시보드** — 전체 프로젝트/세션 수, 최근 세션 목록, 활성 프로젝트 한눈에 보기
- **프로젝트 목록** — 세션 수·최근 활동 기준 정렬, 카드형 뷰
- **세션 뷰어** — 대화 내용을 마크다운 렌더링(GFM), 툴 호출/결과 접기·펴기
- **검색** — 전체 세션 대상 전문 검색, 프로젝트·날짜 범위 필터
- **사용 통계** — 일별·월별 세션 수, 요일/시간대 분포, 모델별 토큰 사용량, 프로젝트별 세션 수, 비용 추정
- **다크/라이트 테마** 전환, **폰트 크기** 조절
- 실행 시 브라우저 자동 오픈

## 설치

### go install (권장)

```bash
go install github.com/pickmoment/claude-reader@latest
claude-reader
```

### 소스에서 빌드

```bash
git clone https://github.com/pickmoment/claude-reader.git
cd claude-reader
go build -o claude-reader .
./claude-reader
```

## 빠른 시작

```bash
go run .
```

5000번 포트부터 시작해 사용 가능한 포트를 자동으로 찾아 실행한다. `~/.claude` 디렉토리를 자동으로 읽는다.

```
Claude Reader running at http://localhost:5000
```

### 옵션

```bash
go run . -port 9090 -dir /path/to/claude
```

| 플래그 | 기본값 | 설명 |
|--------|--------|------|
| `-port` | `5000` | 시작 포트 (사용 중이면 빈 포트를 자동 탐색) |
| `-dir` | `~/.claude` | Claude 데이터 디렉토리 |

### 바이너리 빌드

```bash
go build -o claude-reader .
./claude-reader
```

웹 에셋(HTML/CSS/JS)이 바이너리에 임베드되므로 단일 파일로 배포 가능하다.

## 요구사항

- Go 1.21+
- Claude Code가 생성한 `~/.claude/projects/` 디렉토리

## 아키텍처

세션 파일 파싱은 세 티어로 나뉜다:

| 티어 | 설명 | 사용처 |
|------|------|--------|
| Tier 1 | 디렉토리 스캔만 (JSONL 파싱 없음) | 프로젝트/세션 카운트 |
| Tier 2 | 집계 필드만 파싱 (메시지 구조체 미생성) | 목록 페이지, 통계 |
| Tier 3 | 전체 파싱 (Message/ContentBlock 구조체 생성) | 세션 상세, 검색 |

Tier 2 파싱 결과는 파일 mtime + size 기반으로 캐시하여 페이지 로드마다 재파싱하지 않는다. 디렉토리 스캔은 캐시 없이 매 요청마다 실행해 새로 추가된 세션을 재시작 없이 반영한다.

## 비용 추정

[Anthropic 공개 가격표](https://docs.anthropic.com/en/docs/about-claude/pricing) 기준으로 추정한다. 실제 청구 금액과 다를 수 있다.

| 모델 | Input | Output |
|------|-------|--------|
| Fable 5 / Mythos 5 | $10 / MTok | $50 / MTok |
| Opus 4.5+ | $5 / MTok | $25 / MTok |
| Opus (Claude 3, deprecated) | $15 / MTok | $75 / MTok |
| Sonnet | $3 / MTok | $15 / MTok |
| Haiku 4.5 | $1 / MTok | $5 / MTok |
| Haiku 3.5 | $0.80 / MTok | $4 / MTok |
| Haiku (Claude 3) | $0.25 / MTok | $1.25 / MTok |

캐시 write는 input × 1.25, 캐시 read는 input × 0.1로 계산한다.
