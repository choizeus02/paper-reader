-- +goose Up
create table papers (
  id          text primary key,
  title       text not null,
  filename    text not null,
  page_count  int  not null default 0,
  status      text not null,
  error       text,
  created_at  timestamptz not null default now()
);

create table pages (
  paper_id    text not null references papers(id) on delete cascade,
  page_no     int  not null,
  width       real not null,
  height      real not null,
  image_path  text not null,
  blocks      jsonb not null,
  primary key (paper_id, page_no)
);

create table page_results (
  paper_id        text not null,
  page_no         int  not null,
  status          text not null,
  translation     jsonb,
  explanation     text,
  page_summary    text,
  prompt_version  int  not null default 0,
  error           text,
  updated_at      timestamptz not null default now(),
  primary key (paper_id, page_no),
  foreign key (paper_id, page_no) references pages(paper_id, page_no) on delete cascade
);

create table glossary (
  paper_id   text not null references papers(id) on delete cascade,
  term_en    text not null,
  term_ko    text not null,
  note       text,
  primary key (paper_id, term_en)
);

create table threads (
  id          text primary key,
  paper_id    text not null,
  page_no     int  not null,
  anchor      jsonb not null,
  created_at  timestamptz not null default now(),
  foreign key (paper_id, page_no) references pages(paper_id, page_no) on delete cascade
);
create index threads_page_idx on threads (paper_id, page_no);

create table messages (
  id          bigserial primary key,
  thread_id   text not null references threads(id) on delete cascade,
  role        text not null,
  content     text not null,
  status      text not null,
  created_at  timestamptz not null default now()
);
create index messages_thread_idx on messages (thread_id, id);

-- +goose Down
drop table messages;
drop table threads;
drop table glossary;
drop table page_results;
drop table pages;
drop table papers;
