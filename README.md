# pmpm

함께 쓰는 50분 집중 / 10분 휴식 글자수 타이머.

- `index.html` — 화면 전체. pmpm.exe가 켤 때마다 이 파일(main 브랜치)을 받아 쓰므로, 여기만 고치면 모두에게 자동 반영됨(최대 5분 캐시).
- `main.go` — pmpm.exe 소스(윈도우 전용). 창 크기·항상 위 처리, 화면 파일 받아오기. 바꾸면 exe를 다시 빌드해 배포해야 함.
  - 빌드: `GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-H windowsgui -s -w" -o pmpm.exe .`
- 데이터: Supabase `pmpm_log`(세션 기록), `pmpm_cycle`(공용 시작 기준), `pmpm_secret`(공용 비밀번호). 읽기·쓰기는 비밀번호 확인 함수(`pmpm_list`, `pmpm_put`, `pmpm_cycle_get/set`)로만.
