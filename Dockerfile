FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY engines ./engines
COPY examples ./examples
EXPOSE 8000
CMD ["python", "-m", "uvicorn", "examples.app:app", "--host", "0.0.0.0", "--port", "8000"]
