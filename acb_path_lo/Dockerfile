FROM python:3.12.15-slim-trixie@sha256:2b4f19dae3a777dfc3b76730bda1e82e1f66ab2a2686fa93ca78edbfb4f04ffe

WORKDIR /app

COPY requirements.txt constraints.txt ./
RUN pip install --no-cache-dir -c constraints.txt -r requirements.txt

COPY . ./acb_path_lo

EXPOSE 8000

CMD ["uvicorn", "acb_path_lo.main:app", "--host", "0.0.0.0", "--port", "8000"]
