FROM python:3.12-slim

WORKDIR /app

COPY requirements.txt ./requirements.txt
RUN pip install --no-cache-dir -r requirements.txt

COPY . ./acb_path_lo

EXPOSE 8000

CMD ["uvicorn", "acb_path_lo.main:app", "--host", "0.0.0.0", "--port", "8000"]
