FROM python:3.10-slim
WORKDIR /app
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
COPY prerequisite-analysis/requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
COPY prerequisite-analysis/prereq ./prereq
COPY prerequisite-analysis/web ./web
COPY prerequisite-analysis/testdata/catalog.json ./testdata/catalog.json
USER 65532:65532
CMD ["python", "-m", "prereq.app", "--port", "8094"]
